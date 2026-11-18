package ministers

// Well-known minister IDs. These carry structural meaning (tab ownership,
// seal chain order, ritual routing) and are used throughout the codebase
// instead of raw string literals.
const (
	Secretary  = "secretary"
	Forge      = "forge"
	Judge      = "judge"
	Chancellor = "chancellor"
	War        = "war"
	Rites      = "rites"
	Ruler      = "ruler"
)

// DefaultTabIDs is the ordered list of ministers that get interactive tabs
// in the TUI: Forge, Chancellor, Judge, Secretary.
var DefaultTabIDs = []string{Forge, Secretary, Judge, Chancellor}

// SealChainIDs is the ordered list of minister IDs whose seals form the
// ascension chain: Judge → Chancellor → Ruler.
var SealChainIDs = []string{Judge, Chancellor, Ruler}
