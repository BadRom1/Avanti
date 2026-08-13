// Package web expose l'interface humaine : routes HTTP, rendu serveur avec
// html/template, interactions HTMX (bibliothèque vendorée, aucun CDN), sessions
// via alexedwards/scs et traductions via nicksnyder/go-i18n.
//
// Frontières : c'est un adapter, il appelle les services des domaines et
// internal/platform, et traduit entre HTTP et le vocabulaire métier. Aucun
// domaine ne l'importe, et il n'importe aucune autre famille d'adapters : une
// vue transverse s'assemble en interrogeant chaque domaine, pas en passant par
// adapters/postgres.
package web
