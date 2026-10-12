package tlsfingerprint

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A captured TLS record contains handshake fields in ordered, length-prefixed
// byte vectors rather than the profile representation used for dialing.
// Capture parsing requires a complete ClientHello inside the first record,
// extracts supported lists, and leaves unknown extensions represented by
// their IDs. It does not assemble a ClientHello split across records.
//
// GREASE values vary between handshakes, so profile lists normalize
// recognized GREASE entries to a fixed marker while JA3 excludes those
// entries. The retained legacy version and original list order determine the
// JA3 text before MD5 supplies its conventional fingerprint spelling.
// Optional extension parsers can omit malformed vectors or retain a valid
// prefix, allowing outer framing success to produce a partial capture.

// Record and handshake tags identify the single ClientHello framing accepted
// before capture parsers inspect its declared body and optional extension vectors.
const (
	tlsRecordTypeHandshake  = 22
	tlsHandshakeClientHello = 1
)

// CapturedClientHello holds profile fields extracted from a raw ClientHello.
type CapturedClientHello struct {
	// CipherSuites, Curves, SupportedVersions, KeyShareGroups, and Extensions
	// retain order while replacing recognized GREASE values with 0x0a0a.
	// EnableGREASE records whether those original lists contained GREASE.
	CipherSuites        []uint16 // ordered normalized cipher suite IDs
	Curves              []uint16 // ordered normalized supported group IDs
	PointFormats        []uint16 // ordered advertised point format IDs
	SignatureAlgorithms []uint16 // ordered advertised signature scheme IDs
	ALPNProtocols       []string // ordered advertised application protocol names
	SupportedVersions   []uint16 // ordered normalized TLS version IDs
	KeyShareGroups      []uint16 // ordered normalized key share group IDs
	PSKModes            []uint16 // ordered advertised PSK mode IDs
	Extensions          []uint16 // ordered normalized extension type IDs
	EnableGREASE        bool     // recognized GREASE presence flag
	JA3Raw              string   // conventional comma-separated JA3 text
	JA3Hash             string   // hex MD5 of JA3 text
}

// ParseCapturedClientHello extracts a ClientHello from one complete handshake
// record and returns its captured profile fields. It validates outer record
// and handshake lengths and types, then passes only the declared hello bytes
// to body parsing. Trailing input is ignored; split records are not assembled
// and malformed optional vectors can produce partial captured lists.
func ParseCapturedClientHello(record []byte) (*CapturedClientHello, error) {
	if len(record) < 9 {
		return nil, errors.New("client hello record too short")
	}
	if record[0] != tlsRecordTypeHandshake {
		return nil, fmt.Errorf("unexpected TLS record type %d", record[0])
	}
	recordLen := int(record[3])<<8 | int(record[4])
	if recordLen <= 0 || len(record) < 5+recordLen {
		return nil, errors.New("incomplete TLS record payload")
	}
	body := record[5 : 5+recordLen]
	if len(body) < 4 || body[0] != tlsHandshakeClientHello {
		return nil, errors.New("TLS record does not contain ClientHello")
	}
	helloLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if helloLen <= 0 || len(body) < 4+helloLen {
		return nil, errors.New("incomplete ClientHello payload")
	}
	return parseClientHelloBody(body[4 : 4+helloLen])
}

// parseClientHelloBody reads required ClientHello framing and ordered
// extension IDs before building captured profile lists. Invalid required
// lengths return errors; known optional extension payloads use their own
// parsers and can yield nil or partial lists.
//
// Cipher, extension, group, version, and key-share lists are inspected for
// GREASE before normalization. Profile lists keep a fixed GREASE placeholder,
// while JA3 uses the legacy version and filters GREASE from its participating
// original lists. Returned list storage is newly allocated and does not
// retain the input byte buffer.
func parseClientHelloBody(data []byte) (*CapturedClientHello, error) {
	offset := 0
	legacyVersion, ok := readUint16(data, &offset)
	if !ok {
		return nil, errors.New("missing legacy version")
	}
	if !skip(data, &offset, 32) {
		return nil, errors.New("missing random")
	}
	sessionIDLen, ok := readUint8(data, &offset)
	if !ok || !skip(data, &offset, int(sessionIDLen)) {
		return nil, errors.New("invalid session id")
	}

	cipherSuitesLen, ok := readUint16(data, &offset)
	if !ok || int(cipherSuitesLen)%2 != 0 || offset+int(cipherSuitesLen) > len(data) {
		return nil, errors.New("invalid cipher suites")
	}
	cipherSuitesRaw := readUint16List(data[offset : offset+int(cipherSuitesLen)])
	offset += int(cipherSuitesLen)

	compressionLen, ok := readUint8(data, &offset)
	if !ok || !skip(data, &offset, int(compressionLen)) {
		return nil, errors.New("invalid compression methods")
	}

	var extensionIDsRaw []uint16
	var curvesRaw []uint16
	var pointFormats []uint16
	var signatureAlgorithms []uint16
	var alpnProtocols []string
	var supportedVersionsRaw []uint16
	var keyShareGroupsRaw []uint16
	var pskModes []uint16

	if offset < len(data) {
		extensionsLen, ok := readUint16(data, &offset)
		if !ok || offset+int(extensionsLen) > len(data) {
			return nil, errors.New("invalid extensions block")
		}
		end := offset + int(extensionsLen)
		for offset < end {
			extType, ok := readUint16(data, &offset)
			if !ok {
				return nil, errors.New("invalid extension type")
			}
			extLen, ok := readUint16(data, &offset)
			if !ok || offset+int(extLen) > end {
				return nil, errors.New("invalid extension payload")
			}
			extData := data[offset : offset+int(extLen)]
			offset += int(extLen)
			extensionIDsRaw = append(extensionIDsRaw, extType)
			switch extType {
			case 10:
				curvesRaw = parseSupportedGroups(extData)
			case 11:
				pointFormats = parsePointFormats(extData)
			case 13:
				signatureAlgorithms = parseUint16Vector(extData)
			case 16:
				alpnProtocols = parseALPN(extData)
			case 43:
				supportedVersionsRaw = parseSupportedVersions(extData)
			case 45:
				pskModes = parsePSKModes(extData)
			case 51:
				keyShareGroupsRaw = parseKeyShareGroups(extData)
			}
		}
	}

	// Inspect raw lists before replacing variable GREASE values; the profile
	// presence flag and GREASE-free JA3 then describe the same captured hello.
	enableGREASE := containsGREASE(cipherSuitesRaw) ||
		containsGREASE(extensionIDsRaw) ||
		containsGREASE(curvesRaw) ||
		containsGREASE(supportedVersionsRaw) ||
		containsGREASE(keyShareGroupsRaw)
	captured := &CapturedClientHello{
		CipherSuites:        normalizeGREASEList(cipherSuitesRaw),
		Curves:              normalizeGREASEList(curvesRaw),
		PointFormats:        pointFormats,
		SignatureAlgorithms: signatureAlgorithms,
		ALPNProtocols:       alpnProtocols,
		SupportedVersions:   normalizeGREASEList(supportedVersionsRaw),
		KeyShareGroups:      normalizeGREASEList(keyShareGroupsRaw),
		PSKModes:            pskModes,
		Extensions:          normalizeGREASEList(extensionIDsRaw),
		EnableGREASE:        enableGREASE,
	}
	captured.JA3Raw = buildJA3Raw(legacyVersion, cipherSuitesRaw, extensionIDsRaw, curvesRaw, pointFormats)
	sum := md5.Sum([]byte(captured.JA3Raw))
	captured.JA3Hash = hex.EncodeToString(sum[:])
	return captured, nil
}

// parseSupportedGroups decodes the extension's two-byte-length vector of
// group IDs through parseUint16Vector. Missing, oversized, or odd framing
// returns nil; valid declared bytes keep their order in fresh list storage.
func parseSupportedGroups(data []byte) []uint16 {
	return parseUint16Vector(data)
}

// parsePointFormats decodes the declared one-byte-count point-format vector
// into uint16 values. Missing or oversized framing returns nil; bytes after
// the declared vector are ignored rather than added to the captured list.
func parsePointFormats(data []byte) []uint16 {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	if 1+count > len(data) {
		return nil
	}
	out := make([]uint16, 0, count)
	for _, v := range data[1 : 1+count] {
		out = append(out, uint16(v))
	}
	return out
}

// parseALPN decodes length-prefixed protocol names within the declared ALPN
// vector. An invalid outer length returns nil, and bytes after that vector
// are ignored. Protocol bytes are copied into strings without character-set
// validation.
//
// An empty or overlong name stops parsing and returns names already accepted
// from this vector. Retaining that prefix lets a framed ClientHello expose
// the protocol information preceding a malformed entry instead of discarding
// all optional ALPN observations.
func parseALPN(data []byte) []string {
	if len(data) < 2 {
		return nil
	}
	listLen := int(data[0])<<8 | int(data[1])
	if 2+listLen > len(data) {
		return nil
	}
	offset := 2
	end := 2 + listLen
	out := make([]string, 0, 2)
	for offset < end {
		nameLen := int(data[offset])
		offset++
		// Keep already decoded ALPN names when the next name is malformed,
		// allowing a framed capture to expose available preceding protocol information.
		if nameLen == 0 || offset+nameLen > end {
			return out
		}
		out = append(out, string(data[offset:offset+nameLen]))
		offset += nameLen
	}
	return out
}

// parseSupportedVersions decodes a one-byte-length vector of two-byte TLS
// version IDs. Missing, oversized, or odd framing returns nil; successfully
// declared bytes retain their order and trailing bytes are ignored.
func parseSupportedVersions(data []byte) []uint16 {
	if len(data) < 1 {
		return nil
	}
	vecLen := int(data[0])
	if 1+vecLen > len(data) || vecLen%2 != 0 {
		return nil
	}
	return readUint16List(data[1 : 1+vecLen])
}

// parsePSKModes decodes the one-byte-count PSK mode vector into uint16
// values. Missing or oversized framing returns nil, while a valid vector
// keeps its order and ignores later payload bytes.
func parsePSKModes(data []byte) []uint16 {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	if 1+count > len(data) {
		return nil
	}
	out := make([]uint16, 0, count)
	for _, v := range data[1 : 1+count] {
		out = append(out, uint16(v))
	}
	return out
}

// parseKeyShareGroups extracts group IDs in declared key-share order while
// skipping each entry's key material. An invalid outer length returns nil,
// and returned groups use separate list storage. Bytes after the declared
// vector do not add another accepted share.
//
// A group is appended after its length and complete key payload have passed
// the vector bound check. If an entry is incomplete, preceding accepted
// groups remain available to the outer capture, while the incomplete current
// group is omitted.
func parseKeyShareGroups(data []byte) []uint16 {
	if len(data) < 2 {
		return nil
	}
	vecLen := int(data[0])<<8 | int(data[1])
	if 2+vecLen > len(data) {
		return nil
	}
	offset := 2
	end := 2 + vecLen
	out := make([]uint16, 0, 2)
	for offset < end {
		group, ok := readUint16(data, &offset)
		if !ok {
			// An incomplete key-share entry ends this optional vector while retaining
			// preceding accepted group IDs for the outer captured profile.
			return out
		}
		keyLen, ok := readUint16(data, &offset)
		// Discard the incomplete current key share and retain its preceding
		// groups, because the outer ClientHello framing can still be a valid capture.
		if !ok || offset+int(keyLen) > end {
			return out
		}
		out = append(out, group)
		offset += int(keyLen)
	}
	return out
}

// parseUint16Vector decodes a two-byte-length vector of uint16 values in wire
// order. Missing, oversized, or odd framing returns nil; valid declared bytes
// are decoded into fresh storage and trailing bytes are ignored.
func parseUint16Vector(data []byte) []uint16 {
	if len(data) < 2 {
		return nil
	}
	vecLen := int(data[0])<<8 | int(data[1])
	if 2+vecLen > len(data) || vecLen%2 != 0 {
		return nil
	}
	return readUint16List(data[2 : 2+vecLen])
}

// readUint16List copies complete big-endian uint16 pairs into fresh storage.
// A final unmatched byte is ignored; callers validate declared vector parity
// before using this helper for a complete TLS vector.
func readUint16List(data []byte) []uint16 {
	out := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		out = append(out, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return out
}

// buildJA3Raw joins the legacy version and ordered cipher, extension, group,
// and point-format lists into JA3 text. Each participating list uses
// joinJA3Values to omit GREASE, while other capture fields remain outside
// this fingerprint.
func buildJA3Raw(version uint16, cipherSuites, extensions, curves, pointFormats []uint16) string {
	return strings.Join([]string{
		strconv.Itoa(int(version)),
		joinJA3Values(cipherSuites),
		joinJA3Values(extensions),
		joinJA3Values(curves),
		joinJA3Values(pointFormats),
	}, ",")
}

// joinJA3Values renders ordered non-GREASE IDs as decimal values joined with
// hyphens. Empty input or a list containing only GREASE produces empty text
// for its JA3 component.
func joinJA3Values(values []uint16) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		// GREASE is deliberately variable across handshakes. Omit it from the
		// conventional JA3 component while keeping ordered stable IDs for comparison.
		if isGREASEValue(v) {
			continue
		}
		parts = append(parts, strconv.Itoa(int(v)))
	}
	return strings.Join(parts, "-")
}

// normalizeGREASEList copies values in order and replaces recognized GREASE
// IDs with 0x0a0a, returning nil for empty input. Other numeric IDs retain
// their values in separately allocated list storage.
//
// A GREASE position remains represented while its per-handshake numeric
// value is removed. The stored profile can therefore reuse that position
// when uTLS supplies fresh GREASE bytes during dialing, while JA3 filtering
// separately omits the varying values from fingerprint text.
func normalizeGREASEList(values []uint16) []uint16 {
	if len(values) == 0 {
		return nil
	}
	out := make([]uint16, 0, len(values))
	for _, v := range values {
		// Profile persistence uses one GREASE marker for varying wire values,
		// retaining their positions so dialing can generate its own GREASE bytes.
		if isGREASEValue(v) {
			out = append(out, 0x0a0a)
			continue
		}
		out = append(out, v)
	}
	return out
}

// containsGREASE reports whether any supplied ID matches the TLS GREASE
// pattern.
func containsGREASE(values []uint16) bool {
	return slices.ContainsFunc(values, isGREASEValue)
}

// readUint8 reads one byte at the caller's nonnegative offset and advances it
// on success. Insufficient input returns false without advancing; callers
// retain offset ownership while parsing one buffer.
func readUint8(data []byte, offset *int) (uint8, bool) {
	if *offset >= len(data) {
		return 0, false
	}
	v := data[*offset]
	*offset++
	return v, true
}

// readUint16 reads a big-endian pair at the caller's nonnegative offset and
// advances it on success. Insufficient input returns false without advancing;
// callers validate enclosing vector limits separately.
func readUint16(data []byte, offset *int) (uint16, bool) {
	if *offset+2 > len(data) {
		return 0, false
	}
	v := uint16(data[*offset])<<8 | uint16(data[*offset+1])
	*offset += 2
	return v, true
}

// skip advances the caller's nonnegative offset over n available bytes and
// reports success. Negative n or insufficient input returns false without
// advancing; enclosing-vector limits remain with the parser caller.
func skip(data []byte, offset *int, n int) bool {
	if n < 0 || *offset+n > len(data) {
		return false
	}
	*offset += n
	return true
}
