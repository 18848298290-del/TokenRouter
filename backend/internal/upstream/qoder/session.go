package qoder

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
)

// ServerPublicKeyPEM is the fixed public key extracted from qodercli.
const ServerPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var parsedPubKey *rsa.PublicKey

// AuthIdentity holds the authenticated user identity and tokens.
type AuthIdentity struct {
	Name               string `json:"name"`
	AID                string `json:"aid"`
	UID                string `json:"uid"`
	YxUID              string `json:"yx_uid"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	UserType           string `json:"user_type"`
	SecurityOauthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
}

// MachineIdentity identifies the client machine.
type MachineIdentity struct {
	MachineID    string
	MachineToken string
	MachineType  string
}

// SessionContext holds encrypted parameters and identities used by COSY requests.
type SessionContext struct {
	TempKey       []byte
	CosyKey       string
	Info          string
	Identity      *AuthIdentity
	Machine       *MachineIdentity
	Site          Site
	ClientVersion string
}

func init() {
	block, _ := pem.Decode([]byte(ServerPublicKeyPEM))
	if block == nil {
		panic("qoder: failed to decode PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("qoder: failed to parse public key: %v", err))
	}
	var ok bool
	parsedPubKey, ok = pub.(*rsa.PublicKey)
	if !ok {
		panic("qoder: public key is not RSA")
	}
}

// BuildAuthPayloadJSON marshals the identity as compact JSON.
func BuildAuthPayloadJSON(identity *AuthIdentity) ([]byte, error) {
	return json.Marshal(identity)
}

// BuildPayloadB64WithVersion builds a COSY header payload with the site client version.
func BuildPayloadB64WithVersion(info, requestID, clientVersion string) (string, error) {
	if clientVersion == "" {
		clientVersion = GlobalClientVersion
	}
	payload := map[string]string{
		"cosyVersion": clientVersion,
		"ideVersion":  "",
		"info":        info,
		"requestId":   requestID,
		"version":     "v1",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

// RSAEncrypt encrypts with the server public key using PKCS#1 v1.5.
func RSAEncrypt(data []byte) ([]byte, error) {
	//nolint:staticcheck // Qoder COSY requires PKCS#1 v1.5 for qodercli compatibility.
	return rsa.EncryptPKCS1v15(rand.Reader, parsedPubKey, data)
}

// AESEncrypt uses AES-128-CBC with PKCS#7 padding and key as the IV. The key must be 16 bytes.
func AESEncrypt(data, key []byte) ([]byte, error) {
	// Add a full padding block when the plaintext is already block-aligned.
	blockSize := aes.BlockSize
	padding := blockSize - len(data)%blockSize
	padText := make([]byte, len(data)+padding)
	copy(padText, data)
	for i := len(data); i < len(padText); i++ {
		padText[i] = byte(padding)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	ciphertext := make([]byte, len(padText))
	mode := cipher.NewCBCEncrypter(block, key) // IV = key
	mode.CryptBlocks(ciphertext, padText)
	return ciphertext, nil
}

// NewSessionForSite creates a COSY session for the site.
func NewSessionForSite(identity *AuthIdentity, machine *MachineIdentity, site Site) (*SessionContext, error) {
	profile, err := ProfileForSite(site)
	if err != nil {
		return nil, err
	}
	return NewSessionForProfileWithKey(identity, machine, profile, nil)
}

// NewSessionForProfileWithKey creates a COSY session with the given profile.
// A nil tempKey generates a 16-byte key; a supplied key must be 16 bytes.
// The session retains the identity, machine, and key supplied by the caller.
func NewSessionForProfileWithKey(identity *AuthIdentity, machine *MachineIdentity, profile Profile, tempKey []byte) (*SessionContext, error) {
	normalizedProfile, err := NormalizeProfile(profile)
	if err != nil {
		return nil, err
	}
	if tempKey == nil {
		tempKey = []byte(RandomHex(16))
	}

	encryptedKey, err := RSAEncrypt(tempKey)
	if err != nil {
		return nil, fmt.Errorf("qoder: rsa encrypt temp key: %w", err)
	}
	cosyKey := base64.StdEncoding.EncodeToString(encryptedKey)

	authJSON, err := BuildAuthPayloadJSON(identity)
	if err != nil {
		return nil, fmt.Errorf("qoder: marshal auth payload: %w", err)
	}

	encryptedInfo, err := AESEncrypt(authJSON, tempKey)
	if err != nil {
		return nil, fmt.Errorf("qoder: aes encrypt auth: %w", err)
	}
	info := base64.StdEncoding.EncodeToString(encryptedInfo)

	return &SessionContext{
		TempKey:       tempKey,
		CosyKey:       cosyKey,
		Info:          info,
		Identity:      identity,
		Machine:       machine,
		Site:          normalizedProfile.Site,
		ClientVersion: normalizedProfile.ClientVersion,
	}, nil
}

// AESDecrypt decrypts AES-128-CBC and validates PKCS#7 padding. The 16-byte key is also the IV.
func AESDecrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("qoder: ciphertext is empty")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("qoder: ciphertext is not a multiple of block size")
	}

	plaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, key)
	mode.CryptBlocks(plaintext, ciphertext)

	// Validate every padding byte before removing the padding.
	paddingLen := int(plaintext[len(plaintext)-1])
	if paddingLen > aes.BlockSize || paddingLen == 0 {
		return nil, fmt.Errorf("qoder: invalid padding")
	}
	for i := len(plaintext) - paddingLen; i < len(plaintext); i++ {
		if plaintext[i] != byte(paddingLen) {
			return nil, fmt.Errorf("qoder: invalid padding")
		}
	}
	return plaintext[:len(plaintext)-paddingLen], nil
}
