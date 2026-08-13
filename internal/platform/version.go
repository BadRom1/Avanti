package platform

import (
	"fmt"
	"runtime"
	"strings"
)

// Ces variables sont surchargées à la compilation via -ldflags -X.
// Les valeurs par défaut correspondent à un build local non estampillé.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// BuildInfo décrit l'identité du binaire en cours d'exécution.
type BuildInfo struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
}

// Build renvoie les informations de build du binaire courant.
func Build() BuildInfo {
	return BuildInfo{
		Version:   fallback(version, "dev"),
		Commit:    fallback(commit, "none"),
		Date:      fallback(date, "unknown"),
		GoVersion: runtime.Version(),
	}
}

// String rend les informations de build sur une seule ligne, destinée à
// l'affichage par `avanti --version`.
func (b BuildInfo) String() string {
	return fmt.Sprintf("avanti %s (commit %s, build %s, %s)",
		b.Version, b.Commit, b.Date, b.GoVersion)
}

// fallback remplace une valeur vide ou uniquement composée d'espaces par def,
// afin qu'un -ldflags mal renseigné ne produise pas une ligne de version trouée.
func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}
