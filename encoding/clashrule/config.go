package clashrule

import "github.com/haha12358/geo/encoding/v2raygeo"

func GetClashRule(from v2raygeo.Domain_Type) string {

	switch from {
	case v2raygeo.Domain_Plain:
		return "DOMAIN-KEYWORD,"
	case v2raygeo.Domain_Full:
		return "DOMAIN,"
	case v2raygeo.Domain_Domain:
		return "DOMAIN-SUFFIX,"
	default:
		return ""
	}

}
