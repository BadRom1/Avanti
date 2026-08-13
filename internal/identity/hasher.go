package identity

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/alexedwards/argon2id"
)

// PasswordHash est l'empreinte d'un mot de passe, telle que le domaine la stocke :
// une valeur opaque, que seul un [Hasher] sait interpréter.
//
// Le type existe pour deux raisons. Il empêche de passer par mégarde un mot de
// passe en clair là où une empreinte est attendue, et son [PasswordHash.String]
// masque le contenu : un « %v » sur un [User] ne recopie pas l'empreinte dans un
// journal. Récupérer la valeur réelle demande une conversion explicite en
// string, ce que seul un adapter de persistance a de bonnes raisons de faire.
type PasswordHash string

// String masque l'empreinte. C'est délibérément une perte d'information : les
// usages légitimes de la valeur réelle passent par une conversion explicite.
func (e PasswordHash) String() string {
	if e == "" {
		return "(aucune empreinte)"
	}
	return "(empreinte masquée)"
}

// Empty indique l'absence d'empreinte.
func (e PasswordHash) Empty() bool {
	return e == ""
}

// Hasher transforme un mot de passe en empreinte et vérifie une empreinte.
//
// C'est un port, et il en est un pour une raison de coût : un hachage argon2id
// digne de ce nom prend des dizaines de millisecondes, par construction. Les
// tests unitaires du domaine — ceux qui exercent les rôles, les validations et
// les chemins d'authentification — substituent une implémentation triviale et
// restent instantanés, ce qui les rend utilisables sous `make mutation`.
//
// L'implémentation de production est [Argon2idHasher], dans ce même package :
// le choix de l'algorithme est arrêté par docs/ARCHITECTURE.md §5, il n'a pas à
// être rejouable par configuration.
type Hasher interface {
	// Hash calcule l'empreinte d'un mot de passe. Deux appels sur le même mot
	// de passe donnent deux empreintes différentes : le sel en fait partie.
	Hash(password string) (PasswordHash, error)
	// Verify dit si le mot de passe correspond à l'empreinte. L'erreur est
	// réservée aux empreintes illisibles, pas au simple désaccord.
	Verify(hash PasswordHash, password string) (bool, error)
}

// Paramètres argon2id, alignés sur la deuxième configuration recommandée par la
// RFC 9106 et reprise par l'OWASP : 19 Mio de mémoire, deux passes.
//
// Le parallélisme est figé à 1 plutôt que déduit du nombre de cœurs. Il est
// inscrit dans l'empreinte produite, donc une empreinte calculée sur une machine
// à 16 cœurs doit rester vérifiable après restauration d'une sauvegarde sur un
// petit serveur — ce que la valeur fixe garantit.
const (
	argon2idMemory      = 19 * 1024
	argon2idIterations  = 2
	argon2idParallelism = 1
	argon2idSaltLength  = 16
	argon2idKeyLength   = 32
)

// Argon2idHasher hache les mots de passe en argon2id, l'algorithme retenu par
// docs/ARCHITECTURE.md §5.
type Argon2idHasher struct {
	params *argon2id.Params
}

// NewArgon2idHasher construit le hacheur de production.
//
// Ses paramètres ne sont pas réglables : un exploitant qui pourrait les abaisser
// finirait par le faire pour accélérer une suite de tests, et l'instance
// tournerait ensuite avec ce réglage. Un test qui veut aller vite substitue un
// autre [Hasher], ce qui est visible dans le test.
func NewArgon2idHasher() *Argon2idHasher {
	return &Argon2idHasher{params: &argon2id.Params{
		Memory:      argon2idMemory,
		Iterations:  argon2idIterations,
		Parallelism: argon2idParallelism,
		SaltLength:  argon2idSaltLength,
		KeyLength:   argon2idKeyLength,
	}}
}

// Hash calcule l'empreinte argon2id du mot de passe. L'empreinte renvoyée
// porte ses propres paramètres : relever le coût plus tard n'invalide pas les
// empreintes déjà en base, elles restent vérifiables.
func (h *Argon2idHasher) Hash(password string) (PasswordHash, error) {
	hash, err := argon2id.CreateHash(password, h.params)
	if err != nil {
		return "", fmt.Errorf("hachage argon2id : %w", err)
	}
	return PasswordHash(hash), nil
}

// Verify compare le mot de passe à l'empreinte. La comparaison finale est faite
// en temps constant par la bibliothèque.
func (h *Argon2idHasher) Verify(hash PasswordHash, password string) (bool, error) {
	matches, err := argon2id.ComparePasswordAndHash(password, string(hash))
	if err != nil {
		return false, fmt.Errorf("vérification argon2id : %w", err)
	}
	return matches, nil
}

// generatedPasswordEntropy est le nombre d'octets d'aléa d'un mot de passe
// engendré par [GeneratePassword]. Vingt-quatre octets font 192 bits, écrits
// sur 32 caractères : bien au-delà de ce qu'une attaque hors ligne atteint, et
// encore recopiable à la main si le gestionnaire de mots de passe fait défaut.
const generatedPasswordEntropy = 24

// GeneratePassword tire un mot de passe aléatoire conforme à la politique.
//
// C'est le domaine qui l'engendre, et non la ligne de commande, parce que c'est
// lui qui sait ce qu'un mot de passe acceptable veut dire — la longueur minimale
// vit ici. L'alphabet est celui de base64 URL, sans remplissage : il ne contient
// ni caractère qu'un shell interprète, ni couple de glyphes confondus par une
// police de terminal.
func GeneratePassword() (string, error) {
	password, err := randomSecret(generatedPasswordEntropy)
	if err != nil {
		return "", err
	}
	// Garde-fou contre une baisse future de l'entropie : un mot de passe engendré
	// qui ne passerait pas la politique du domaine est un bug, pas un incident.
	if err := CheckPassword(password); err != nil {
		return "", fmt.Errorf("mot de passe engendré non conforme : %w", err)
	}
	return password, nil
}

// randomSecret tire un secret imprononçable de la longueur d'entropie
// demandée, écrit en base64 URL sans remplissage.
func randomSecret(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("tirage d'un secret aléatoire : %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
