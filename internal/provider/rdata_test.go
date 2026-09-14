package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToRdata(t *testing.T) {
	tests := []struct {
		typ, target string
		want        map[string]any
		wantErr     string
	}{
		{"A", "10.0.0.1", map[string]any{"address": "10.0.0.1"}, ""},
		{"A", " 10.0.0.1 ", map[string]any{"address": "10.0.0.1"}, ""},
		{"A", "::1", nil, "not an IPv4"},
		{"A", "nope", nil, "not an IPv4"},
		{"AAAA", "2001:db8::1", map[string]any{"address": "2001:db8::1"}, ""},
		{"AAAA", "10.0.0.1", nil, "not an IPv6"},
		{"CNAME", "target.example.com", map[string]any{"cname": "target.example.com."}, ""},
		{"CNAME", "target.example.com.", map[string]any{"cname": "target.example.com."}, ""},
		{"CNAME", "", nil, "must not be empty"},
		{"NS", "ns1.example.com", map[string]any{"dname": "ns1.example.com."}, ""},
		{"NS", "", nil, "must not be empty"},
		{"TXT", `"heritage=external-dns,external-dns/owner=x"`, map[string]any{"text": `"heritage=external-dns,external-dns/owner=x"`}, ""},
		{"MX", "10 mail.example.com", map[string]any{"preference": int64(10), "exchange": "mail.example.com."}, ""},
		{"MX", "mail.example.com", nil, "must be"},
		{"MX", "x mail.example.com", nil, "preference"},
		{"MX", "70000 mail.example.com", nil, "preference"},
		{"SRV", "10 20 5060 sip.example.com", map[string]any{"priority": int64(10), "weight": int64(20), "port": int64(5060), "target": "sip.example.com."}, ""},
		{"SRV", "10 20 sip.example.com", nil, "must be"},
		{"SRV", "10 x 5060 sip.example.com", nil, "weight"},
		{"PTR", "x", nil, "unsupported record type"},
	}
	for _, tc := range tests {
		t.Run(tc.typ+"/"+tc.target, func(t *testing.T) {
			got, err := toRdata(tc.typ, tc.target)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFromRdata(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		rdata   map[string]any
		want    string
		wantErr string
	}{
		{"A", "A", map[string]any{"address": "10.0.0.1"}, "10.0.0.1", ""},
		{"AAAA", "AAAA", map[string]any{"address": "2001:db8::1"}, "2001:db8::1", ""},
		{"A missing", "A", map[string]any{}, "", `missing "address"`},
		{"A wrong type", "A", map[string]any{"address": 1}, "", "want string"},
		{"CNAME", "CNAME", map[string]any{"cname": "t.example.com."}, "t.example.com", ""},
		{"NS", "NS", map[string]any{"dname": "ns1.example.com."}, "ns1.example.com", ""},
		{"TXT", "TXT", map[string]any{"text": `"v=spf1 -all"`}, `"v=spf1 -all"`, ""},
		{"TXT missing", "TXT", map[string]any{}, "", `missing "text"`},
		{"MX float", "MX", map[string]any{"preference": float64(10), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX int", "MX", map[string]any{"preference": 10, "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX int32", "MX", map[string]any{"preference": int32(10), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX int64", "MX", map[string]any{"preference": int64(10), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX float32", "MX", map[string]any{"preference": float32(10), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX uint16", "MX", map[string]any{"preference": uint16(10), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX json.Number", "MX", map[string]any{"preference": json.Number("10"), "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX string", "MX", map[string]any{"preference": "10", "exchange": "mail.example.com."}, "10 mail.example.com", ""},
		{"MX bad number", "MX", map[string]any{"preference": true, "exchange": "mail.example.com."}, "", "want number"},
		{"MX missing pref", "MX", map[string]any{"exchange": "mail.example.com."}, "", `missing "preference"`},
		{"MX missing exchange", "MX", map[string]any{"preference": 10}, "", `missing "exchange"`},
		{"SRV", "SRV", map[string]any{"priority": float64(10), "weight": float64(20), "port": float64(5060), "target": "sip.example.com."}, "10 20 5060 sip.example.com", ""},
		{"SRV missing port", "SRV", map[string]any{"priority": 10, "weight": 20, "target": "sip."}, "", `missing "port"`},
		{"SRV missing target", "SRV", map[string]any{"priority": 10, "weight": 20, "port": 1}, "", `missing "target"`},
		{"unsupported", "SOA", map[string]any{}, "", "unsupported record type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fromRdata(tc.typ, tc.rdata)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	cases := map[string]string{
		"A":     "192.0.2.1",
		"AAAA":  "2001:db8::2",
		"CNAME": "alias.example.com",
		"NS":    "ns.example.com",
		"TXT":   `"some text with spaces"`,
		"MX":    "5 mx.example.com",
		"SRV":   "1 2 3 srv.example.com",
	}
	for typ, target := range cases {
		rdata, err := toRdata(typ, target)
		require.NoError(t, err, typ)
		// Simulate a JSON round trip (numbers become float64).
		raw, _ := json.Marshal(rdata)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(raw, &decoded))
		back, err := fromRdata(typ, decoded)
		require.NoError(t, err, typ)
		assert.Equal(t, target, back, typ)
	}
}

func TestQuoteTXT(t *testing.T) {
	assert.Equal(t, `"abc"`, quoteTXT("abc"))
	assert.Equal(t, `"abc"`, quoteTXT(`"abc"`))
	assert.Equal(t, `"v=spf1 -all"`, quoteTXT("v=spf1 -all"))
	assert.Equal(t, `"say \"hi\""`, quoteTXT(`say "hi"`))
	assert.Equal(t, `""`, quoteTXT(""))
	assert.Equal(t, `"\""`, quoteTXT(`"`))
	assert.Equal(t, `"a\\""`, quoteTXT(`a\"`), "string ending in an escaped quote is not already quoted")
}

func TestSupportedType(t *testing.T) {
	for _, s := range SupportedTypes {
		assert.True(t, supportedType(s))
	}
	assert.False(t, supportedType("SOA"))
	assert.False(t, supportedType("PTR"))
}

func TestUnquoteTXT(t *testing.T) {
	assert.Equal(t, "abc", unquoteTXT(`"abc"`))
	assert.Equal(t, "abc", unquoteTXT("abc"))
	assert.Equal(t, `say "hi"`, unquoteTXT(`"say \"hi\""`))
	assert.Equal(t, "", unquoteTXT(`""`))
	assert.Equal(t, `"a" "b"`, unquoteTXT(`"a" "b"`), "multi-string stays as is")
	assert.Equal(t, `a\"`, unquoteTXT(`a\"`))
	for _, s := range []string{"abc", "v=spf1 -all", `say "hi"`, ""} {
		assert.Equal(t, s, unquoteTXT(quoteTXT(s)), "round trip %q", s)
	}
}
