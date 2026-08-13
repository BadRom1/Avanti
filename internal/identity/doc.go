// Package identity porte le domaine transverse de l'identité et des accès :
// comptes, mots de passe (hachés en argon2id), rôles, et les scopes qui bornent
// ce qu'un porteur de jeton peut faire — qu'il s'agisse d'un humain sur l'UI web
// ou d'un agent IA passant par le serveur MCP.
//
// # Ce que le domaine expose
//
// [User] est le compte tel qu'il est stocké ; [Actor] est ce que les autres
// couches reçoivent pour autoriser une action. La distinction est délibérée :
// un User porte une empreinte de mot de passe et des horodatages, un Actor ne
// porte qu'un identifiant et un jeu de scopes. Les domaines métier recevront un
// Actor en paramètre — c'est ce qui leur évite d'importer identity et préserve
// R1 de docs/ARCHITECTURE.md.
//
// [AccountService] rassemble les cas d'usage : créer, authentifier, changer un
// mot de passe, désactiver, lister. Il s'appuie sur deux ports, [UserRepository]
// pour la persistance et [Hasher] pour le hachage.
//
// # Frontières
//
// Ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Les autres domaines ne l'importent pas non plus : ils
// reçoivent l'identité de l'appelant en paramètre, jamais en allant
// l'interroger. La mécanique OAuth 2.1 n'est pas ici : c'est un adapter
// (internal/adapters/mcp) qui la branche sur ce domaine.
package identity
