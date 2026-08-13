// Package logging construit le journal structuré de l'application.
//
// Un seul *slog.Logger est fabriqué au démarrage puis passé en paramètre à ceux
// qui en ont besoin. Le dépôt n'a délibérément pas de journal global : un
// domaine qui voudrait journaliser sans qu'on le lui ait demandé enfreindrait R1
// (voir docs/ARCHITECTURE.md), et un test qui doit couper le bruit d'un
// composant n'a alors qu'à lui en donner un autre.
package logging

import (
	"io"
	"log/slog"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
)

// New construit le journal décrit par cfg et l'écrit dans out.
//
// Le format JSON est celui de la production : il est indexable par un collecteur
// de journaux. Le format texte est celui du développement : il se lit dans un
// terminal sans outil intermédiaire.
func New(out io.Writer, cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}

	var handler slog.Handler
	if cfg.LogFormat == config.LogJSON {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}

	return slog.New(handler)
}

// Discard renvoie un journal qui n'écrit nulle part, à l'usage des tests qui
// instancient un composant journalisant sans vouloir en lire la sortie.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
