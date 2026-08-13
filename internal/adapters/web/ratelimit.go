package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Réglages du garde-fou anti-force-brute.
const (
	// failuresBeforeBlock est le nombre de tentatives ratées tolérées avant que le
	// couple compte + adresse IP ne soit mis en attente.
	failuresBeforeBlock = 5
	// blockWindow est la durée du blocage, et aussi la durée au bout de
	// laquelle un compteur d'échecs s'oublie.
	blockWindow = 15 * time.Minute
	// trackedCap borne le nombre de couples suivis simultanément. Sans lui, une
	// attaque qui fait tourner les adresses et les emails ferait grandir la carte
	// sans fin — le garde-fou deviendrait lui-même la faille.
	trackedCap = 4096
)

// loginLimiter ralentit les tentatives de connexion répétées sur un même
// compte depuis une même adresse.
//
// Il est volontairement minimal, et il faut savoir ce qu'il n'est pas :
//
//   - il vit en mémoire. Un redémarrage remet les compteurs à zéro, et deux
//     instances ne partageraient rien. Avanti est un binaire unique servant deux
//     personnes : un magasin partagé n'apporterait ici qu'un aller-retour réseau ;
//   - la clé comprend l'adresse IP vue de la connexion TCP, jamais un en-tête
//     X-Forwarded-For. Faire confiance à un en-tête sans savoir quel proxy est
//     devant permettrait à l'attaquant de choisir sa propre clé, donc de n'être
//     jamais bloqué. Derrière un reverse proxy, toutes les requêtes partagent donc
//     l'adresse du proxy et le garde-fou dégénère en limite par compte — ce qui
//     reste l'essentiel de ce qu'on veut ;
//   - il ne prétend pas arrêter une attaque distribuée. Ce qui la rend coûteuse,
//     c'est argon2id ; ce garde-fou ne fait qu'enlever l'intérêt d'un script qui
//     essaie mille mots de passe sur un compte connu.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]failedAttempts
	clock    func() time.Time
}

// failedAttempts est le compteur d'un couple compte + adresse.
type failedAttempts struct {
	failures int
	last     time.Time
}

// newLimiter construit le garde-fou. clock nil signifie time.Now — les
// tests en injectent une pour ne pas avoir à attendre quinze minutes.
func newLimiter(clock func() time.Time) *loginLimiter {
	if clock == nil {
		clock = time.Now
	}
	return &loginLimiter{
		attempts: make(map[string]failedAttempts),
		clock:    clock,
	}
}

// key identifie la tentative : le compte visé et l'adresse d'où elle vient.
//
// L'email est ramené en minuscules pour que « Romain@… » et « romain@… » comptent
// ensemble, et tronqué pour qu'une saisie démesurée ne devienne pas une clé
// démesurée.
func (l *loginLimiter) key(email string, r *http.Request) string {
	account := strings.ToLower(strings.TrimSpace(email))
	if len(account) > 254 {
		account = account[:254]
	}

	return account + "|" + callerAddr(r)
}

// callerAddr extrait l'adresse IP de la connexion. Une adresse illisible
// est rendue telle quelle : elle sert de clé, pas de preuve.
func callerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// blocked dit si la clé est en attente, et pour combien de temps encore.
func (l *loginLimiter) blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	counter, ok := l.attempts[key]
	if !ok || counter.failures < failuresBeforeBlock {
		return 0, false
	}

	remaining := blockWindow - l.clock().Sub(counter.last)
	if remaining <= 0 {
		// La fenêtre est passée : le compteur s'oublie et la clé repart à neuf.
		delete(l.attempts, key)
		return 0, false
	}

	return remaining, true
}

// failure enregistre une tentative ratée.
func (l *loginLimiter) failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()

	counter, ok := l.attempts[key]
	if !ok {
		l.evict(now)
	} else if now.Sub(counter.last) >= blockWindow {
		// Compteur périmé : on repart de zéro plutôt que d'accumuler des échecs
		// vieux de plusieurs heures.
		counter.failures = 0
	}

	counter.failures++
	counter.last = now
	l.attempts[key] = counter
}

// success efface le compteur d'une clé : une connexion réussie annule les
// tentatives ratées qui l'ont précédée.
func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.attempts, key)
}

// evict borne la taille de la carte avant d'y ajouter une clé.
//
// Les compteurs périmés partent d'abord. S'il n'y en a pas assez, la plus
// ancienne entrée est évincée : mieux vaut perdre le suivi du couple le moins
// récent que laisser une attaque tournante faire grossir la carte, ou l'inverse —
// refuser d'ajouter une clé désarmerait le garde-fou pour tous les autres.
//
// À appeler le verrou tenu.
func (l *loginLimiter) evict(now time.Time) {
	if len(l.attempts) < trackedCap {
		return
	}

	for key, counter := range l.attempts {
		if now.Sub(counter.last) >= blockWindow {
			delete(l.attempts, key)
		}
	}
	if len(l.attempts) < trackedCap {
		return
	}

	var (
		oldestKey  string
		oldestSeen time.Time
	)
	for key, counter := range l.attempts {
		if oldestKey == "" || counter.last.Before(oldestSeen) {
			oldestKey, oldestSeen = key, counter.last
		}
	}
	delete(l.attempts, oldestKey)
}
