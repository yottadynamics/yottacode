package cost

// CatalogVersion is the date the price tables in this package were
// last verified against each provider's public pricing page. Bumped
// manually on edit; surfaced in the /usage footer so users know how
// fresh the catalog is.
//
// When prices drift between bumps, /usage estimates drift too — the
// "~" prefix on every dollar figure flags the number as an estimate,
// but users with strict budgeting should still verify against the
// provider's billing dashboard.
const CatalogVersion = "2026-05-28"
