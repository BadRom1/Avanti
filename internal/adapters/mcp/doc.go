// Package mcp expose l'interface agent : un serveur Model Context Protocol bâti
// sur le SDK Go officiel (github.com/modelcontextprotocol/go-sdk), protégé par un
// serveur OAuth 2.1 embarqué (ory/fosite). Les tools MCP sont la projection des
// cas d'usage des domaines, bornés par les scopes du domaine identity.
//
// Frontières : c'est un adapter, il appelle les services des domaines et
// internal/platform. Aucun domaine ne l'importe, et il n'importe aucune autre
// famille d'adapters : leur seul point de rencontre est cmd/avanti.
package mcp
