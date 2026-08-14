// Package mcp expose l'interface agent d'Avanti : un serveur Model Context
// Protocol bâti sur le SDK Go officiel (github.com/modelcontextprotocol/go-sdk),
// servi en transport HTTP streamable sur [ServerPath].
//
// # Authentification : jeton OAuth, jamais de session
//
// Chaque requête porte un jeton d'accès en en-tête Authorization, vérifié par le
// port [identity.TokenVerifier] — la seule porte de cet adapter vers l'identité :
// la mécanique OAuth 2.1 (fosite) vit dans adapters/web, et les deux familles ne
// se voient que par cmd/avanti (R4 de docs/ARCHITECTURE.md). Il n'y a ici ni
// session, ni cookie, ni protection CSRF, et c'est raisonné plutôt que commode :
// ces défenses protègent un navigateur porteur de session contre un site tiers
// qui la ferait agir ; un point de terminaison machine n'a pas de session à
// défendre, et ce qui l'autorise est le jeton de la requête — le modèle des
// points de terminaison machine du serveur d'autorisation (ARCHITECTURE §5).
//
// Une requête sans jeton valable reçoit 401 avec l'en-tête WWW-Authenticate qui
// pointe vers le document Protected Resource Metadata (RFC 9728), servi
// publiquement sur [ProtectedResourceMetadataPath] : c'est par lui qu'un client
// MCP découvre le serveur d'autorisation. Un jeton valable sans le scope « mcp »
// reçoit 403 (insufficient_scope au sens de la RFC 6750) : le canal est refusé,
// pas le jeton.
//
// # Tools : la projection des cas d'usage, bornée par les scopes
//
// Chaque tool est gardé par un scope de domaine en plus du scope mcp déjà exigé
// à l'entrée : un acteur sans le scope reçoit une erreur qui le nomme
// (« scope devis:read requis »), jamais un résultat vide. Les montants circulent
// en centimes entiers (int64), les dates au format AAAA-MM-JJ. Les noms et
// descriptions des tools sont en français — c'est l'user-visible de ce canal,
// comme les sorties de la CLI — et les erreurs métier des domaines remontent
// avec leur message français : ce sont des refus explicables. Les pannes
// techniques, elles, se journalisent côté serveur et rendent une erreur
// générique. Restent en anglais, et c'est assumé, les chaînes que le SDK émet
// lui-même — corps des 401/403/405, erreurs de validation de schéma JSON :
// elles sont hors de portée du code d'Avanti et ne divulguent rien, le 401
// restant indistinct quel que soit le motif du refus.
//
// Deux limites de périmètre sont des décisions de cadrage de la V1 :
//
//   - aucun contenu binaire ne passe par MCP — documents_liste rend les
//     métadonnées seulement ;
//   - aucun envoi n'est effectué — assurance_preparer_envoi assemble le dossier
//     et le dit en toutes lettres ; la transmission à l'assurance reste un geste
//     humain, et cet adapter n'a aucun port d'envoi de mail.
package mcp
