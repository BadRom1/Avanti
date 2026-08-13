// Package identity porte le domaine transverse de l'identité et des accès :
// comptes, mots de passe (hachés en argon2id), rôles, et les scopes qui bornent
// ce qu'un porteur de jeton peut faire — qu'il s'agisse d'un humain sur l'UI web
// ou d'un agent IA passant par le serveur MCP.
//
// Contenu attendu : les entités et invariants du domaine, les ports (dépôt de
// comptes, hachage de secrets, horloge) et les services applicatifs
// (authentification, changement de mot de passe, vérification de scope). La
// mécanique OAuth 2.1 elle-même n'est pas ici : c'est un adapter
// (internal/adapters/mcp) qui la branche sur ce domaine.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Les autres domaines ne l'importent pas non plus : ils
// reçoivent l'identité de l'appelant en paramètre (un ActorID et ses scopes),
// jamais en allant l'interroger.
package identity
