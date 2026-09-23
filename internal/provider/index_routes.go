package provider

// ValidateIndexRoute checks the adapters' actual symbol tables. Massive and mock
// accept general I: symbols; their upstream availability is checked on request.
func ValidateIndexRoute(name, symbol string) error {
	switch name {
	case "longbridge":
		if _, ok := longbridgeIndexRoutes[symbol]; !ok {
			return unsupportedIndexSymbol("Longbridge", symbol, longbridgeIndexRoutes)
		}
	case "fmp":
		if _, ok := fmpIndexRoutes[symbol]; !ok {
			return unsupportedIndexSymbol("FMP", symbol, fmpIndexRoutes)
		}
	}
	return nil
}
