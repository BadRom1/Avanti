package document

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// ID identifie une pièce du dossier. C'est un UUID version 4 dans sa forme
// canonique.
//
// Le type est distinct de string, et distinct des identifiants des autres
// domaines (devis.ID, identity.ID…) : le compilateur refuse ainsi de confondre
// l'identifiant d'une pièce avec celui d'un devis, ce qui est l'objet de R2 de
// docs/ARCHITECTURE.md.
type ID string

// String rend l'identifiant sous sa forme canonique.
func (i ID) String() string {
	return string(i)
}

// NewID tire un identifiant aléatoire.
//
// L'aléa vient de crypto/rand, et ce n'est pas une précaution de style :
// l'identifiant d'une pièce est aussi sa clé de stockage et figure dans l'URL
// de téléchargement. Un générateur prévisible rendrait devinable l'adresse de
// documents d'assurance et de finances. L'erreur est propagée plutôt
// qu'ignorée — un crypto/rand en panne est une raison de refuser le dépôt, pas
// de continuer avec un identifiant douteux.
func NewID() (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("tirage d'un identifiant de document : %w", err)
	}

	// Version 4, variante RFC 4122 : les deux quartets imposés par la norme.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	text := hex.EncodeToString(raw[:])

	return ID(text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]), nil
}

// ActeurID désigne la personne qui a téléversé une pièce.
//
// C'est une référence faible vers le domaine identity, au sens de R2 de
// docs/ARCHITECTURE.md : une valeur transportée pour la traçabilité, jamais un
// pointeur vers un compte. Le domaine ne sait pas la résoudre, et n'a pas à le
// savoir — il consigne qui a agi, l'interface saura le nommer.
type ActeurID string

// String rend l'identifiant de l'acteur.
func (a ActeurID) String() string {
	return string(a)
}
