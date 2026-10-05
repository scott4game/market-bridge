package provider

import (
	"regexp"
	"strings"
)

// CommonStockExclusion returns an auditable exclusion, never a guessed replacement type.
// Match security descriptions, not company-name fragments such as "Preferred Bank".
var securityNameConflicts = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`(?i)\b(preferred (stock|shares?|securities)|preference shares?)\b`), "name describes preferred equity"},
	{regexp.MustCompile(`(?i)\b((senior|subordinated|junior|unsecured|secured) notes?|notes? due|bonds? due|debentures)\b`), "name describes debt"},
	{regexp.MustCompile(`(?i)\b(american (depositary|depository) (shares?|receipts?)|sponsored adrs?)\b`), "name describes ADR"},
	{regexp.MustCompile(`(?i)\b(etfs?|exchange[- ]traded (funds?|notes?))\b`), "name describes exchange-traded product"},
	{regexp.MustCompile(`(?i)\b(warrants? (to purchase|exercisable|expiring)|units? (each |consisting))|\bwarrants?$`), "name describes warrants or composite units"},
}

func CommonStockExclusion(p SecurityProfile) string {
	if !p.Active || !strings.EqualFold(p.Locale, "us") || !strings.EqualFold(p.Market, "stocks") {
		return "not an active US stock"
	}
	if !strings.EqualFold(p.Type, "CS") {
		return "not common stock: " + p.Type
	}
	switch p.PrimaryExchange {
	case "XNAS", "XNYS", "XASE", "BATS":
	default:
		return "unverified or non-listed primary exchange"
	}
	if strings.TrimSpace(p.Name) == "" {
		return "security name unavailable for classification"
	}
	for _, rule := range securityNameConflicts {
		if rule.pattern.MatchString(p.Name) {
			return rule.reason
		}
	}
	return ""
}
