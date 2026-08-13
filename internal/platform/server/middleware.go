package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// RequestIDHeader porte l'identifiant de corrélation, à l'aller comme au retour.
// C'est le nom qu'emploient les reverse proxys courants ; le reprendre évite de
// perdre la trace à la frontière.
const RequestIDHeader = "X-Request-Id"

// maxInheritedRequestIDLen borne l'identifiant accepté d'un client. Sans borne,
// un appelant hostile ferait grossir chaque ligne de journal à sa guise.
const maxInheritedRequestIDLen = 64

// requestIDContextKey est un type privé : personne d'autre que ce package ne
// peut fabriquer la clé, donc personne ne peut écraser la valeur par accident.
type requestIDContextKey struct{}

// RequestIDFromContext renvoie l'identifiant de la requête en cours, ou une
// chaîne vide hors d'une requête HTTP.
func RequestIDFromContext(ctx context.Context) string {
	id, ok := ctx.Value(requestIDContextKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

// requestID attribue un identifiant à chaque requête, en reprenant celui du
// client s'il est présentable, et le renvoie dans la réponse pour qu'un
// utilisateur puisse le citer dans un rapport d'incident.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(RequestIDHeader))
		if id == "" {
			id = newRequestID()
		}

		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}

// sanitizeRequestID ne garde un identifiant venu du client que s'il est court et
// composé de caractères inoffensifs. Tout le reste est rejeté : un identifiant
// contenant un retour à la ligne polluerait les journaux d'entrées fabriquées.
func sanitizeRequestID(candidate string) string {
	if candidate == "" || len(candidate) > maxInheritedRequestIDLen {
		return ""
	}

	for _, r := range candidate {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ""
		}
	}

	return candidate
}

// newRequestID tire un identifiant aléatoire. Huit octets suffisent : il s'agit
// de corréler des lignes de journal sur une fenêtre de quelques heures, pas de
// garantir l'unicité universelle.
func newRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Inatteignable en pratique : depuis Go 1.24, crypto/rand.Read ne rend
		// jamais d'erreur. Servir une requête sans identifiant reste préférable
		// à refuser de la servir.
		return "sans-identifiant"
	}
	return hex.EncodeToString(raw[:])
}

// accessLog écrit une ligne par requête servie, une fois la réponse partie.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			logger.LogAttrs(r.Context(), levelFor(recorder.status), "requête servie",
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.Int64("bytes", recorder.written),
				slog.Duration("duration", time.Since(started)),
				slog.String("remote_addr", r.RemoteAddr))
		})
	}
}

// levelFor évite qu'un journal en niveau info noie les erreurs serveur, et qu'un
// journal en niveau warn laisse passer une avalanche de 404 sans rien dire.
func levelFor(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// recoverPanic transforme une panique de gestionnaire en 500, plutôt qu'en
// arrêt du processus. La pile part dans le journal, jamais dans la réponse.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// http.ErrAbortHandler est la façon documentée d'interrompre une
				// réponse : la rattraper masquerait une intention délibérée.
				if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(recovered)
				}

				logger.LogAttrs(r.Context(), slog.LevelError, "panique dans un gestionnaire HTTP",
					slog.String("request_id", RequestIDFromContext(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())))

				http.Error(w, "Erreur interne du serveur.", http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// responseRecorder retient le code et la taille de la réponse pour le journal
// d'accès, sans rien changer à ce qui est envoyé au client.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(payload []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(payload)
	r.written += int64(n)
	// L'erreur remonte nue : le contrat de io.Writer veut qu'un intermédiaire
	// transmette celle de la couche en dessous sans l'habiller.
	return n, err
}

// Unwrap rend l'enveloppe transparente à http.NewResponseController, qui est la
// façon actuelle d'atteindre Flush ou Hijack au travers d'un intergiciel.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
