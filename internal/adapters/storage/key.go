package storage

import (
	"fmt"
	"regexp"
)

// keyPattern est la seule forme de clé que les stockages acceptent : un UUID
// canonique en minuscules, exactement ce que document.NewID produit.
//
// C'est la première défense contre la traversée de chemin, et elle est choisie
// de préférence à un nettoyage : une clé nettoyée est une clé transformée,
// dont on doit prouver qu'aucune transformation ne redevient dangereuse ; une
// clé validée par une forme fermée n'a rien à prouver — « ../x », un chemin
// absolu, un UUID suivi d'un suffixe, tout ce qui n'est pas exactement un UUID
// est refusé avant de toucher au moindre chemin ou à la moindre URL. Le
// stockage disque double cette validation par os.Root, qui borne les
// opérations au répertoire au niveau du système : deux défenses indépendantes,
// et aucun #nosec dans le paquet.
var keyPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// checkKey refuse toute clé qui n'a pas exactement la forme d'un UUID.
func checkKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("storage : clé %q refusée, forme UUID canonique exigée", key)
	}
	return nil
}
