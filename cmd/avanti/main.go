// Commande avanti : point d'entrée unique de l'application.
//
// C'est le seul endroit du dépôt autorisé à connaître à la fois les domaines et
// les adapters : il lit la configuration, choisit les implémentations concrètes
// des ports et les injecte dans les services de domaine. Pour l'instant il ne
// fait qu'afficher son identité de build — le serveur arrive avec le socle
// applicatif.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Romain-Badino/Avanti/internal/platform"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "avanti: %v\n", err)
		os.Exit(1)
	}
}

// run contient le corps du programme, isolé de os.Exit pour rester testable.
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("avanti", flag.ContinueOnError)
	fs.SetOutput(stderr)

	showVersion := fs.Bool("version", false, "affiche la version puis quitte")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}

	// Tant que le serveur n'existe pas, tout appel se comporte comme --version.
	_ = showVersion

	if _, err := fmt.Fprintln(stdout, platform.Build()); err != nil {
		return fmt.Errorf("écriture de la version : %w", err)
	}

	return nil
}
