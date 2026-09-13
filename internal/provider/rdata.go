package provider

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"sigs.k8s.io/external-dns/endpoint"
)

// SupportedTypes lists the record types this provider manages.
var SupportedTypes = []string{
	endpoint.RecordTypeA,
	endpoint.RecordTypeAAAA,
	endpoint.RecordTypeCNAME,
	endpoint.RecordTypeTXT,
	endpoint.RecordTypeSRV,
	endpoint.RecordTypeMX,
	endpoint.RecordTypeNS,
}

func supportedType(t string) bool {
	for _, s := range SupportedTypes {
		if s == t {
			return true
		}
	}
	return false
}

// toRdata converts an external-dns target into UDDI rdata for the type.
func toRdata(recordType, target string) (map[string]any, error) {
	target = strings.TrimSpace(target)
	switch recordType {
	case endpoint.RecordTypeA:
		addr, err := netip.ParseAddr(target)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("A target %q is not an IPv4 address", target)
		}
		return map[string]any{"address": addr.String()}, nil
	case endpoint.RecordTypeAAAA:
		addr, err := netip.ParseAddr(target)
		if err != nil || !addr.Is6() {
			return nil, fmt.Errorf("AAAA target %q is not an IPv6 address", target)
		}
		return map[string]any{"address": addr.String()}, nil
	case endpoint.RecordTypeCNAME:
		if target == "" {
			return nil, fmt.Errorf("CNAME target must not be empty")
		}
		return map[string]any{"cname": fqdn(target)}, nil
	case endpoint.RecordTypeNS:
		if target == "" {
			return nil, fmt.Errorf("NS target must not be empty")
		}
		return map[string]any{"dname": fqdn(target)}, nil
	case endpoint.RecordTypeTXT:
		return map[string]any{"text": target}, nil
	case endpoint.RecordTypeMX:
		fields := strings.Fields(target)
		if len(fields) != 2 {
			return nil, fmt.Errorf("MX target %q must be \"<preference> <exchange>\"", target)
		}
		pref, err := parseUint16(fields[0])
		if err != nil {
			return nil, fmt.Errorf("MX target %q: preference: %w", target, err)
		}
		return map[string]any{"preference": pref, "exchange": fqdn(fields[1])}, nil
	case endpoint.RecordTypeSRV:
		fields := strings.Fields(target)
		if len(fields) != 4 {
			return nil, fmt.Errorf("SRV target %q must be \"<priority> <weight> <port> <target>\"", target)
		}
		nums := make([]int64, 3)
		for i, name := range []string{"priority", "weight", "port"} {
			v, err := parseUint16(fields[i])
			if err != nil {
				return nil, fmt.Errorf("SRV target %q: %s: %w", target, name, err)
			}
			nums[i] = v
		}
		return map[string]any{"priority": nums[0], "weight": nums[1], "port": nums[2], "target": fqdn(fields[3])}, nil
	}
	return nil, fmt.Errorf("unsupported record type %q", recordType)
}

// fromRdata converts UDDI rdata back into an external-dns target.
func fromRdata(recordType string, rdata map[string]any) (string, error) {
	switch recordType {
	case endpoint.RecordTypeA, endpoint.RecordTypeAAAA:
		return requireString(rdata, "address")
	case endpoint.RecordTypeCNAME:
		s, err := requireString(rdata, "cname")
		return unfqdn(s), err
	case endpoint.RecordTypeNS:
		s, err := requireString(rdata, "dname")
		return unfqdn(s), err
	case endpoint.RecordTypeTXT:
		v, ok := rdata["text"]
		if !ok {
			return "", fmt.Errorf("rdata missing %q", "text")
		}
		return fmt.Sprint(v), nil
	case endpoint.RecordTypeMX:
		pref, err := requireNumber(rdata, "preference")
		if err != nil {
			return "", err
		}
		ex, err := requireString(rdata, "exchange")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %s", pref, unfqdn(ex)), nil
	case endpoint.RecordTypeSRV:
		var nums [3]int64
		for i, name := range []string{"priority", "weight", "port"} {
			v, err := requireNumber(rdata, name)
			if err != nil {
				return "", err
			}
			nums[i] = v
		}
		t, err := requireString(rdata, "target")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d %d %s", nums[0], nums[1], nums[2], unfqdn(t)), nil
	}
	return "", fmt.Errorf("unsupported record type %q", recordType)
}

func requireString(rdata map[string]any, key string) (string, error) {
	v, ok := rdata[key]
	if !ok || v == nil {
		return "", fmt.Errorf("rdata missing %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("rdata %q is %T, want string", key, v)
	}
	return s, nil
}

func requireNumber(rdata map[string]any, key string) (int64, error) {
	v, ok := rdata[key]
	if !ok || v == nil {
		return 0, fmt.Errorf("rdata missing %q", key)
	}
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint16:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case string:
		return strconv.ParseInt(n, 10, 64)
	}
	return 0, fmt.Errorf("rdata %q is %T, want number", key, v)
}

func parseUint16(s string) (int64, error) {
	v, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number in 0..65535", s)
	}
	return int64(v), nil
}

// fqdn appends the trailing dot required by UDDI for hostnames in rdata.
func fqdn(name string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// unfqdn strips the trailing dot external-dns does not expect.
func unfqdn(name string) string {
	return strings.TrimSuffix(name, ".")
}

// quoteTXT wraps a TXT value in double quotes unless it already is, so UDDI
// stores it as a single character-string. This is the form external-dns's
// TXT registry already produces, so Records and the plan agree.
func quoteTXT(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && !strings.HasSuffix(s, `\"`) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
