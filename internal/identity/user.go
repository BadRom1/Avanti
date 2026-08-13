package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ID identifie un compte. C'est un UUID version 4 dans sa forme canonique.
//
// Le type est distinct de string, et distinct des identifiants des autres
// domaines (devis.ID, finance.ID…) : le compilateur refuse ainsi de confondre
// l'identifiant d'un compte avec celui d'un devis, ce qui est l'objet de R2 de
// docs/ARCHITECTURE.md.
type ID string

// String rend l'identifiant sous sa forme canonique.
func (i ID) String() string {
	return string(i)
}

// NewID tire un identifiant de compte aléatoire.
//
// L'aléa vient de crypto/rand : un identifiant de compte finit dans des URLs et
// des journaux, un générateur prévisible en ferait une information devinable.
// L'erreur est propagée plutôt qu'ignorée — un crypto/rand en panne est une
// raison de refuser de créer le compte, pas de continuer avec un identifiant
// douteux.
func NewID() (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("tirage d'un identifiant de compte : %w", err)
	}

	// Version 4, variante RFC 4122 : les deux quartets imposés par la norme.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	text := hex.EncodeToString(raw[:])

	return ID(text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]), nil
}

// User est un compte tel qu'il est stocké.
//
// Il porte l'empreinte du mot de passe, que [PasswordHash.String] masque pour
// qu'un « %v » malheureux ne la recopie pas dans un journal. Les couches qui
// n'ont besoin que d'autoriser une action reçoivent un [Actor], pas un User.
type User struct {
	// ID identifie le compte.
	ID ID
	// Email est l'identifiant de connexion, unique et toujours normalisé au sens
	// de [NormalizeEmail].
	Email string
	// DisplayName est le nom montré dans l'interface. Il n'a aucun rôle
	// d'identification et peut changer librement.
	DisplayName string
	// PasswordHash est l'empreinte du mot de passe. Le domaine ne l'interprète
	// jamais : seul un [Hasher] sait ce qu'il y a dedans.
	PasswordHash PasswordHash
	// Role détermine les scopes du compte.
	Role Role
	// Active vaut faux pour un compte désactivé, qui ne peut plus se connecter.
	// Un compte est désactivé plutôt que supprimé : les actions qu'il a signées
	// dans les autres domaines continuent de le désigner.
	Active bool
	// CreatedAt est la date de création du compte.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification du compte.
	UpdatedAt time.Time
}

// Actor construit l'identité d'autorisation du compte.
//
// Un compte désactivé donne un acteur anonyme, sans aucun scope : la
// désactivation vaut retrait des droits, même si un chemin d'appel oubliait de
// vérifier [User.Active].
func (u User) Actor() Actor {
	if !u.Active {
		return Actor{}
	}
	return NewActor(u.ID, u.Role)
}
