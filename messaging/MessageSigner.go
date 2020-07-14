// Package messaging for signing and encryption of messages
package messaging

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/iotdomain/iotdomain-go/types"
	"github.com/sirupsen/logrus"
	"gopkg.in/square/go-jose.v2"
)

// MessageSigner for signing and verifying of signed and encrypted messages
type MessageSigner struct {
	getPublicKey func(address string) *ecdsa.PublicKey
	messenger    IMessenger
	signMessages bool              // flag, sign outgoing messages. Default is true. Disable for testing
	signingKey   *ecdsa.PrivateKey // private key for signing
}

// VerifySignedMessage parses and verifies the message signature
// as per standard, the sender and signer of the message is in the message 'Sender' field. If the
// Sender field is missing then the 'address' field contains the publisher.
//  or 'address' field
func (signer *MessageSigner) VerifySignedMessage(rawMessage string, object interface{}) (isSigned bool, err error) {
	isSigned, err = VerifySignature(rawMessage, object, signer.getPublicKey)
	return isSigned, err
}

// PublishObject encapsulates the message object in a payload, signs the message, and sends it.
//  If an encryption key is provided then the signed message will be encrypted.
//  The object to publish will be marshalled to JSON and signed by this publisher
func (signer *MessageSigner) PublishObject(address string, retained bool, object interface{}, encryptionKey *ecdsa.PublicKey) error {
	// payload, err := json.Marshal(object)
	payload, err := json.MarshalIndent(object, " ", " ")
	if err != nil {
		logrus.Errorf("Publisher.publishMessage: Error marshalling message for address %s: %s", address, err)
		return err
	}
	if encryptionKey != nil {
		err = signer.PublishEncrypted(address, retained, string(payload), encryptionKey)
	} else {
		err = signer.PublishSigned(address, retained, string(payload))
	}
	return err
}

// Subscribe to messages on the given address
func (signer *MessageSigner) Subscribe(address string, handler func(address string, message string)) {
	signer.messenger.Subscribe(address, handler)
}

// Unsubscribe to messages on the given address
func (signer *MessageSigner) Unsubscribe(address string, handler func(address string, message string)) {
	signer.messenger.Unsubscribe(address, handler)
}

// PublishEncrypted sign and encrypts the payload and publish the resulting message on the given address
// Signing only happens if the publisher's signingMethod is set to SigningMethodJWS
func (signer *MessageSigner) PublishEncrypted(
	address string, retained bool, payload string, publicKey *ecdsa.PublicKey) error {
	var err error
	message := payload
	// first sign, then encrypt as per RFC
	if signer.signMessages {
		message, err = CreateJWSSignature(string(payload), signer.signingKey)
	}
	emessage, err := EncryptMessage(message, publicKey)
	err = signer.messenger.Publish(address, retained, emessage)
	return err
}

// PublishSigned sign the payload and publish the resulting message on the given address
// Signing only happens if the publisher's signingMethod is set to SigningMethodJWS
func (signer *MessageSigner) PublishSigned(
	address string, retained bool, payload string) error {
	var err error

	// default is unsigned
	message := payload

	if signer.signMessages {
		message, err = CreateJWSSignature(string(payload), signer.signingKey)
		if err != nil {
			logrus.Errorf("Publisher.publishMessage: Error signing message for address %s: %s", address, err)
		}
	}
	err = signer.messenger.Publish(address, retained, message)
	return err
}

// NewMessageSigner creates a new instance for signing and verifying published messages
func NewMessageSigner(
	signMessages bool,
	getPublicKey func(address string) *ecdsa.PublicKey,
	messenger IMessenger,
	signingKey *ecdsa.PrivateKey,
) *MessageSigner {

	signer := &MessageSigner{
		getPublicKey: getPublicKey,
		messenger:    messenger,
		signMessages: signMessages,
		signingKey:   signingKey, // private key for signing
	}
	return signer
}

/*
 *  Helper Functions for signing and verification
 */

// CreateEcdsaSignature creates a ECDSA256 signature from the payload using the provided private key
// This returns a base64url encoded signature
func CreateEcdsaSignature(payload string, privateKey *ecdsa.PrivateKey) string {
	if privateKey == nil {
		return ""
	}
	hashed := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, hashed[:])
	if err != nil {
		return ""
	}
	sig, err := asn1.Marshal(ECDSASignature{r, s})
	return base64.URLEncoding.EncodeToString(sig)
}

// CreateJWSSignature signs the payload using JSE ES256 and return the JSE compact serialized message
func CreateJWSSignature(payload string, privateKey *ecdsa.PrivateKey) (string, error) {
	joseSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: privateKey}, nil)
	signedObject, err := joseSigner.Sign([]byte(payload))
	if err != nil {
		return "", err
	}
	// serialized := signedObject.FullSerialize()
	serialized, err := signedObject.CompactSerialize()
	return serialized, err
}

// DecryptMessage deserializes and decrypts the message using JWE
// This returns the decrypted message, or the input message if the message was not encrypted
func DecryptMessage(serialized string, privateKey *ecdsa.PrivateKey) (isEncrypted bool, message string, err error) {
	message = serialized
	decrypter, err := jose.ParseEncrypted(serialized)
	if err == nil {
		dmessage, err := decrypter.Decrypt(privateKey)
		message = string(dmessage)
		return true, message, err
	}
	return false, message, err
}

// EncryptMessage encrypts and serializes the message using JWE
func EncryptMessage(message string, publicKey *ecdsa.PublicKey) (serialized string, err error) {

	recpnt := jose.Recipient{Algorithm: jose.ECDH_ES, Key: publicKey}

	encrypter, err := jose.NewEncrypter(jose.A128CBC_HS256, recpnt, nil)

	if err != nil {
		return message, err
	}

	jwe, err := encrypter.Encrypt([]byte(message))
	if err != nil {
		return message, err
	}
	serialized, _ = jwe.CompactSerialize()
	return serialized, err
}

// SignEncodeIdentity returns a base64URL encoded ECDSA256 signature of the publisher identity.
// Used in creating or updating a publisher's identity.
func SignEncodeIdentity(ident *types.PublisherIdentityMessage, privKey *ecdsa.PrivateKey) string {
	signingKey := jose.SigningKey{Algorithm: jose.ES256, Key: privKey}
	joseSigner, _ := jose.NewSigner(signingKey, nil)
	payload, _ := json.Marshal(ident)
	jwsObject, _ := joseSigner.Sign(payload)
	sig := jwsObject.Signatures[0].Signature
	sigStr := base64.URLEncoding.EncodeToString(sig)
	return sigStr
}

// VerifyEcdsaSignature the payload using the base64url encoded signature and public key
// payload is a text or base64 encoded raw data
// signatureB64urlEncoded is the ecdsa 256 URL encoded signature
func VerifyEcdsaSignature(payload string, signatureB64urlEncoded string, publicKey *ecdsa.PublicKey) bool {
	var rs ECDSASignature
	signature, err := base64.URLEncoding.DecodeString(signatureB64urlEncoded)
	if err != nil {
		return false
	}
	if _, err = asn1.Unmarshal(signature, &rs); err != nil {
		return false
	}

	hashed := sha256.Sum256([]byte(payload))
	return ecdsa.Verify(publicKey, hashed[:], rs.R, rs.S)
}

// VerifyJWSMessage verifies a signed message and returns its payload
// message is the message to verify
// publicKey from the signer. This must be known to verify the message.
func VerifyJWSMessage(message string, publicKey *ecdsa.PublicKey) (payload string, err error) {
	jwsSignature, err := jose.ParseSigned(message)
	if err != nil {
		return "", err
	}
	payloadB, err := jwsSignature.Verify(publicKey)
	return string(payloadB), err
}

// VerifySignature verifies if a message is JWS signed. If signed then the signature is verified
// using the 'Sender' or 'Address' attributes to determine the public key to verify with. To verify correctly,
// the sender has to be a known publisher and verified with the DSS.
//
// getPublicKey is a lookup function for providing the public key from the given sender address.
//  it should only provide a public key if the publisher is known and verified by the DSS, or
//  if this zone does not use a DSS (publisher are protected through message bus ACLs)
//
// The rawMessage is json unmarshalled into the given object.
//
// This returns a flag if the message was signed and if so, an error if the verification failed
func VerifySignature(rawMessage string, object interface{}, getPublicKey func(address string) *ecdsa.PublicKey) (isSigned bool, err error) {

	jwsSignature, err := jose.ParseSigned(rawMessage)
	if err != nil {
		// message is (probably) not signed, try to unmarshal it directly
		err = json.Unmarshal([]byte(rawMessage), object)
		return false, err
	}
	payload := jwsSignature.UnsafePayloadWithoutVerification()
	err = json.Unmarshal([]byte(payload), object)
	if err != nil {
		// message doesn't have a json payload
		return true, err
	}
	// determine who the sender is
	reflObject := reflect.ValueOf(object).Elem()
	reflSender := reflObject.FieldByName("Sender")
	if !reflSender.IsValid() {
		reflSender = reflObject.FieldByName("Address")
		if !reflSender.IsValid() {
			err = errors.New("VerifySender: object doesn't have a Sender or Address field")
			return false, err
		}
	}
	sender := reflSender.String()
	if sender == "" {
		err := errors.New("VerifySender: Missing sender information in message")
		return true, err
	}
	// verify the message signature using the sender's public key
	if getPublicKey == nil {
		err := errors.New("VerifySender: Missing method to get public key for sender " + sender)
		return true, err
	}
	publicKey := getPublicKey(sender)
	if publicKey == nil {
		err := errors.New("VerifySender: No public key available for sender " + sender)
		return true, err
	}

	_, err = jwsSignature.Verify(publicKey)
	return true, err
}
