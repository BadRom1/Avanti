package server

import (
	"context"
	"log/slog"
	"net/http"
)

// Les sondes répondent en texte brut ASCII, sans passer par le catalogue de
// traductions : leur lecteur est un orchestrateur ou une commande curl, pas un
// utilisateur. C'est le seul endroit du dépôt où une chaîne visible échappe à
// l'internationalisation, et c'est délibéré.
const (
	liveBody      = "ok"
	readyBody     = "ready"
	notReadyBody  = "not ready"
	plainTextType = "text/plain; charset=utf-8"
)

// handleLive répond tant que le processus tourne et que sa boucle HTTP répond.
//
// Elle ne touche pas à la base à dessein : c'est une sonde de vivacité, et
// redémarrer le processus parce que PostgreSQL est momentanément indisponible
// ne ferait qu'ajouter une panne à une autre.
func handleLive(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, logger, http.StatusOK, liveBody)
	})
}

// handleReady dit si l'instance peut recevoir du trafic — ce qui, ici, veut dire
// que PostgreSQL répond. Une sonde absente vaut « rien à vérifier ».
func handleReady(logger *slog.Logger, ready ReadinessCheck) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready == nil {
			writePlain(w, logger, http.StatusOK, readyBody)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := ready(ctx); err != nil {
			// Le détail de l'erreur reste dans le journal : le corps de la
			// réponse est public et n'a pas à décrire l'infrastructure.
			logger.LogAttrs(ctx, slog.LevelWarn, "sonde de disponibilité en échec",
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("error", err.Error()))

			writePlain(w, logger, http.StatusServiceUnavailable, notReadyBody)
			return
		}

		writePlain(w, logger, http.StatusOK, readyBody)
	})
}

func writePlain(w http.ResponseWriter, logger *slog.Logger, status int, body string) {
	w.Header().Set("Content-Type", plainTextType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	// Une écriture ratée signifie que le client est parti avant la fin. Ce n'est
	// pas un incident de service : la trace reste au niveau debug.
	if _, err := w.Write([]byte(body + "\n")); err != nil {
		logger.Debug("réponse de sonde non transmise", slog.String("error", err.Error()))
	}
}
