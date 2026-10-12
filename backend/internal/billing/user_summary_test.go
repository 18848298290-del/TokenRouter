package billing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

// TestParseNotifyEmails_EmptyString passes empty saved recipient text. It
// requires nil recipients so absent settings do not introduce addresses or
// fabricated contact records.
func TestParseNotifyEmails_EmptyString(t *testing.T) {
	result := ParseNotifyEmails("")
	require.Nil(t, result)
}

// TestParseNotifyEmails_EmptyArray passes a saved empty JSON array. It
// requires nil recipients so an empty saved contact list remains empty at the
// billing wrapper.
func TestParseNotifyEmails_EmptyArray(t *testing.T) {
	result := ParseNotifyEmails("[]")
	require.Nil(t, result)
}

// TestParseNotifyEmails_Null passes JSON null through recipient parsing. It
// checks an empty result, covering the accepted null representation without
// requiring a particular empty-slice allocation.
func TestParseNotifyEmails_Null(t *testing.T) {
	result := ParseNotifyEmails("null")
	require.Empty(t, result)
}

// TestParseNotifyEmails_WhitespaceOnly passes recipient text containing spaces
// alone. It checks a nil result after trimming so an effectively absent value
// contributes no contacts.
func TestParseNotifyEmails_WhitespaceOnly(t *testing.T) {
	result := ParseNotifyEmails("   ")
	require.Nil(t, result)
}

// TestParseNotifyEmails_OldFormat parses a historical array containing two
// address strings. It checks order, addresses and false verification/delivery
// flags so legacy contacts need separate verification before sending.
func TestParseNotifyEmails_OldFormat(t *testing.T) {
	raw := `["alice@example.com", "bob@example.com"]`
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 2)

	require.Equal(t, "alice@example.com", result[0].Email)
	require.False(t, result[0].Verified, "old format emails should default to unverified")
	require.False(t, result[0].Disabled)

	require.Equal(t, "bob@example.com", result[1].Email)
	require.False(t, result[1].Verified)
	require.False(t, result[1].Disabled)
}

// TestParseNotifyEmails_OldFormat_SkipsEmptyEntries parses historical
// addresses mixed with empty and whitespace-only strings. It checks that the
// two nonempty addresses retain their supplied order after filtering.
func TestParseNotifyEmails_OldFormat_SkipsEmptyEntries(t *testing.T) {
	raw := `["alice@example.com", "", "  ", "bob@example.com"]`
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 2)
	require.Equal(t, "alice@example.com", result[0].Email)
	require.Equal(t, "bob@example.com", result[1].Email)
}

// TestParseNotifyEmails_NewFormat parses recipient records with different
// verification and disabled flags. It checks both records retain the supplied
// addresses and flags for later eligibility filtering.
func TestParseNotifyEmails_NewFormat(t *testing.T) {
	raw := `[{"email":"alice@example.com","verified":true,"disabled":false},{"email":"bob@example.com","verified":false,"disabled":true}]`
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 2)

	require.Equal(t, "alice@example.com", result[0].Email)
	require.True(t, result[0].Verified)
	require.False(t, result[0].Disabled)

	require.Equal(t, "bob@example.com", result[1].Email)
	require.False(t, result[1].Verified)
	require.True(t, result[1].Disabled)
}

// TestParseNotifyEmails_NewFormat_SingleEntry parses one verified contact
// record. It checks the one returned address and its verification flag so
// single-record settings use the same record representation.
func TestParseNotifyEmails_NewFormat_SingleEntry(t *testing.T) {
	raw := `[{"email":"solo@example.com","verified":true,"disabled":false}]`
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 1)
	require.Equal(t, "solo@example.com", result[0].Email)
	require.True(t, result[0].Verified)
}

// TestParseNotifyEmails_InvalidJSON supplies malformed JSON as saved recipient
// text. It requires nil recipients so an undecodable setting cannot create a
// partially parsed contact list.
func TestParseNotifyEmails_InvalidJSON(t *testing.T) {
	result := ParseNotifyEmails(`{not valid json`)
	require.Nil(t, result)
}

// TestParseNotifyEmails_InvalidJSONObject supplies a single JSON object where
// an array is expected. It requires nil recipients so that unsupported
// representation is not accepted as a contact list.
func TestParseNotifyEmails_InvalidJSONObject(t *testing.T) {
	result := ParseNotifyEmails(`{"email":"a@b.com"}`)
	require.Nil(t, result)
}

// TestParseNotifyEmails_WhitespacePadding parses an address array surrounded
// by whitespace. It checks the returned address so padding around valid saved
// JSON does not discard the contact.
func TestParseNotifyEmails_WhitespacePadding(t *testing.T) {
	raw := `  ["padded@example.com"]  `
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 1)
	require.Equal(t, "padded@example.com", result[0].Email)
}

// TestMarshalNotifyEmails_EmptySlice encodes an allocated empty recipient
// slice. It checks the literal empty JSON array so settings writes have a
// stable representation for no contacts.
func TestMarshalNotifyEmails_EmptySlice(t *testing.T) {
	result := contact.MarshalNotifyEmails([]NotifyEmailSummary{})
	require.Equal(t, "[]", result)
}

// TestMarshalNotifyEmails_NilSlice encodes a nil recipient slice. It checks
// the literal empty JSON array so missing in-memory contacts are written using
// the same empty-list representation.
func TestMarshalNotifyEmails_NilSlice(t *testing.T) {
	result := contact.MarshalNotifyEmails(nil)
	require.Equal(t, "[]", result)
}

// TestMarshalNotifyEmails_SingleEntry encodes one contact with delivery flags
// and parses the result. It checks address/flag JSON members and the returned
// record equality across that round trip.
func TestMarshalNotifyEmails_SingleEntry(t *testing.T) {
	entries := []NotifyEmailSummary{
		{Email: "test@example.com", Verified: true, Disabled: false},
	}
	result := contact.MarshalNotifyEmails(entries)
	require.Contains(t, result, `"email":"test@example.com"`)
	require.Contains(t, result, `"verified":true`)
	require.Contains(t, result, `"disabled":false`)

	parsed := ParseNotifyEmails(result)
	require.Len(t, parsed, 1)
	require.Equal(t, entries[0], parsed[0])
}

// TestMarshalNotifyEmails_MultipleEntries encodes two records with different
// verification and disabled flags. It parses the JSON and checks the two
// records retain their values and supplied order.
func TestMarshalNotifyEmails_MultipleEntries(t *testing.T) {
	entries := []NotifyEmailSummary{
		{Email: "a@example.com", Verified: true, Disabled: false},
		{Email: "b@example.com", Verified: false, Disabled: true},
	}
	result := contact.MarshalNotifyEmails(entries)

	parsed := ParseNotifyEmails(result)
	require.Len(t, parsed, 2)
	require.Equal(t, entries[0], parsed[0])
	require.Equal(t, entries[1], parsed[1])
}

// TestMarshalNotifyEmails_RoundTrip_NewFormat encodes and reparses two
// recipient records. It checks complete slice equality so saved addresses and
// both delivery flags survive this exercised round trip.
func TestMarshalNotifyEmails_RoundTrip_NewFormat(t *testing.T) {
	original := []NotifyEmailSummary{
		{Email: "x@example.com", Verified: true, Disabled: true},
		{Email: "y@example.com", Verified: false, Disabled: false},
	}
	marshalled := contact.MarshalNotifyEmails(original)
	parsed := ParseNotifyEmails(marshalled)
	require.Equal(t, original, parsed)
}

// TestParseNotifyEmails_MixedOldFormatWithWhitespace parses a legacy address
// with surrounding spaces inside the string. It checks the trimmed address so
// subsequent notification matching receives the intended mailbox.
func TestParseNotifyEmails_MixedOldFormatWithWhitespace(t *testing.T) {
	raw := `["  alice@example.com  "]`
	result := ParseNotifyEmails(raw)
	require.Len(t, result, 1)
	require.Equal(t, "alice@example.com", result[0].Email)
}
