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

// session.go constructs encrypted COSY sessions and request identity payloads.
// COSY authentication places a JSON identity inside AES-CBC ciphertext and
// encrypts the temporary AES key with the server's RSA key. The resulting Info
// and CosyKey strings travel together in Gateway signatures, while the request
// payload header adds a request ID and the site's client version.
//
// Session construction retains the caller's identity, machine, and supplied
// temporary-key references. Info is encrypted from the identity at construction
// time, so later identity mutation does not rebuild that snapshot. Callers keep
// those shared inputs stable while requests use the session. AES helpers use the
// key as a sixteen-byte IV as required by this integration, and decryption
// verifies every padding byte before returning plaintext.

// ServerPublicKeyPEM contains the RSA public key used by the Qoder client
// protocol to wrap each temporary AES key.
const ServerPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// parsedPubKey holds the RSA key parsed during initialization and shared by all
// session-key encryption calls.
var parsedPubKey *rsa.PublicKey

// AuthIdentity contains the user, organization, and tokens serialized into
// encrypted COSY authentication.
type AuthIdentity struct {
	Name               string `json:"name"`                 // authenticated display name
	AID                string `json:"aid"`                  // COSY account identifier
	UID                string `json:"uid"`                  // COSY user identifier
	YxUID              string `json:"yx_uid"`               // Gateway alternate UID
	OrganizationID     string `json:"organization_id"`      // selected organization identifier
	OrganizationName   string `json:"organization_name"`    // selected organization name
	UserType           string `json:"user_type"`            // Qoder account classification
	SecurityOauthToken string `json:"security_oauth_token"` // inference access token
	RefreshToken       string `json:"refresh_token"`        // credential refresh token
}

// MachineIdentity carries the machine fields used by site-specific request
// header construction.
type MachineIdentity struct {
	MachineID    string // client machine identifier
	MachineToken string // client machine token
	MachineType  string // client machine type
}

// SessionContext retains the encrypted credentials and shared inputs used by
// Gateway requests.
type SessionContext struct {
	// TempKey supplies AES encryption while CosyKey wraps those same bytes with RSA
	// and Info holds encrypted identity JSON. Gateway signing carries both encoded
	// results together. TempKey borrows the supplied key slice when one is
	// provided. Callers keep its sixteen bytes stable while the session is used.
	TempKey []byte // AES key bytes
	CosyKey string // base64 RSA ciphertext
	Info    string // base64 identity ciphertext
	// Identity borrows the caller's pointer, while Info snapshots its JSON during
	// construction. Updating this pointer requires rebuilding the encrypted
	// session.
	Identity *AuthIdentity // retained authentication identity
	// Machine borrows the caller's identity pointer. Request header builders read
	// its fields later, so callers coordinate any mutation with session use.
	Machine       *MachineIdentity // retained machine identity
	Site          Site             // selected request site
	ClientVersion string           // header client version
}

// init parses the embedded RSA public key before any session is constructed.
// Invalid PEM, a malformed PKIX key, or a non-RSA key panics during package
// initialization because session encryption depends on this fixed protocol key.
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

// BuildAuthPayloadJSON returns compact JSON for the supplied authentication
// identity.
func BuildAuthPayloadJSON(identity *AuthIdentity) ([]byte, error) {
	return json.Marshal(identity)
}

// BuildPayloadB64WithVersion encodes the identity information, request ID, and
// client version header. An empty version selects the global version; the
// returned standard base64 string is signed and placed in COSY Authorization.
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

// RSAEncrypt encrypts bytes with the fixed server key using PKCS#1 v1.5. API
// failures, including oversized input or FIPS-only rejection, return to
// session construction.
//
// The pinned Go runtime normally uses its internal secure random source, whose
// operating-system read failure terminates execution. With
// GODEBUG=cryptocustomrand=1, RSA honors the supplied crypto/rand.Reader and
// can return that reader's error.
func RSAEncrypt(data []byte) ([]byte, error) {
	//nolint:staticcheck // Qoder COSY requires PKCS#1 v1.5 for qodercli compatibility.
	return rsa.EncryptPKCS1v15(rand.Reader, parsedPubKey, data)
}

// AESEncrypt returns AES-CBC ciphertext after adding PKCS#7 padding to
// plaintext. Callers supply exactly sixteen key bytes because the same slice is
// used as the IV. Invalid AES key sizes return errors; AES-supported keys of
// other lengths can panic during IV validation.
//
// Separate padded and ciphertext storage keeps input bytes available to the
// caller. An aligned plaintext receives a complete padding block, letting
// AESDecrypt distinguish the original length from trailing plaintext bytes.
func AESEncrypt(data, key []byte) ([]byte, error) {
	// Pad into fresh storage because callers may retain the plaintext slice or
	// spare capacity behind it. CBC encryption then writes into separate ciphertext
	// storage for the session envelope.
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

// NewSessionForSite constructs encrypted credentials using the selected site's
// profile defaults. Unknown sites return an error; identity and machine pointers
// are retained by the session, and construction creates a temporary key. Callers
// provide identity and machine pointers usable by the later request-header
// builder.
func NewSessionForSite(identity *AuthIdentity, machine *MachineIdentity, site Site) (*SessionContext, error) {
	profile, err := ProfileForSite(site)
	if err != nil {
		return nil, err
	}
	return NewSessionForProfileWithKey(identity, machine, profile, nil)
}

// NewSessionForProfileWithKey constructs encrypted authentication strings and
// retains the supplied identities. Nil tempKey generates sixteen ASCII
// hexadecimal bytes; callers supply exactly sixteen bytes when providing a key
// and keep that slice, identity, and machine stable while using the session.
//
// RSA encryption produces CosyKey, while AES-CBC encryption of the compact
// identity produces Info. The profile supplies the normalized site and version
// for later signing. Info snapshots identity values at construction, so changing
// the retained identity does not update its encrypted representation. Callers
// supply identity and machine pointers needed by later inference headers; this
// constructor retains them as given, including nil.
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

// AESDecrypt returns AES-CBC plaintext after validating PKCS#7 padding. Callers
// supply exactly sixteen key bytes because the key is also the IV; AES-supported
// keys of other lengths can panic during IV validation. Empty or unaligned
// ciphertext returns an error before block decryption.
//
// The final byte selects padding length, which must fit one block and be
// positive. Every selected padding byte is then checked before slicing the
// allocated plaintext, preventing malformed padding from silently removing valid
// trailing data.
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

	// The final byte selects the padding length. Verify all selected bytes before
	// slicing so corrupt ciphertext cannot silently remove valid trailing
	// plaintext.
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
