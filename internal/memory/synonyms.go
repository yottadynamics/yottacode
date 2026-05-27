package memory

// Expand returns the stemmed input token plus any known synonyms.
// The first element is always the input itself. Tokens not in any
// synonym group return a single-element slice. All entries are
// pre-stemmed so callers must stem before calling.
func Expand(stemmed string) []string {
	if group, ok := synonymIndex[stemmed]; ok {
		return group
	}
	return []string{stemmed}
}

// synonymIndex maps each stemmed token to its full synonym group
// (including itself). Built at init time from synonymGroups.
var synonymIndex map[string][]string

// synonymGroups defines bidirectional synonym families. Every member
// expands to every other member. Entries are in Porter-stemmed form
// so they match the output of Stem().
var synonymGroups = [][]string{
	{"test", "spec", "assert", "check", "verif"},
	{"mock", "fake", "stub", "doubl"},
	{"databas", "db", "datastor", "sql", "sqlite"},
	{"config", "configur", "prefer", "option"},
	{"error", "fail", "issu"},
	{"depend", "packag", "modul", "librari"},
	{"deploy", "releas", "ship", "publish"},
	{"auth", "authent", "login", "credenti", "token"},
	{"permiss", "allow", "deni", "sandbox", "restrict"},
	{"refactor", "cleanup", "rewrit", "migrat"},
	{"doc", "document", "readm", "comment"},
	{"git", "commit", "branch", "merg", "rebas"},
	{"build", "compil", "link", "bundl"},
	{"cach", "memo", "store", "persist"},
	{"api", "endpoint", "rout", "handler", "request"},
}

func init() {
	synonymIndex = make(map[string][]string, len(synonymGroups)*5)
	for _, group := range synonymGroups {
		for _, member := range group {
			synonymIndex[member] = group
		}
	}
}
