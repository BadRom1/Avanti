// Les doublures des tests du domaine. Elles remplacent les deux ports —
// persistance et hachage — par ce qu'il faut de plus simple pour que les tests
// portent sur les règles du domaine, et sur elles seules.
package identity_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// plainHasher est un [identity.Hasher] instantané.
//
// Il préfixe le mot de passe au lieu de le hacher : une empreinte reste ainsi
// distincte d'un mot de passe en clair — un test qui les confondrait échouerait —
// tout en gardant la suite du domaine exerçable des milliers de fois par seconde.
// La lenteur d'argon2id est sa qualité en production et son défaut ici, ce qui
// est précisément la raison d'être du port.
type plainHasher struct {
	// verifies compte les appels, pour vérifier l'égalité des temps de réponse
	// de l'authentification en nombre d'opérations plutôt qu'en microsecondes —
	// une horloge n'aurait rien de fiable sur une machine de CI partagée.
	verifies atomic.Int64
	// unreadable contient les empreintes que Verify refuse de lire, pour
	// exercer le chemin d'erreur.
	unreadable map[identity.PasswordHash]bool
}

const plainPrefix = "trivial:"

func (h *plainHasher) Hash(password string) (identity.PasswordHash, error) {
	return identity.PasswordHash(plainPrefix + password), nil
}

func (h *plainHasher) Verify(hash identity.PasswordHash, password string) (bool, error) {
	h.verifies.Add(1)

	if h.unreadable[hash] {
		return false, errors.New("empreinte illisible")
	}
	if !strings.HasPrefix(string(hash), plainPrefix) {
		return false, fmt.Errorf("empreinte sans préfixe %q", plainPrefix)
	}

	return string(hash) == plainPrefix+password, nil
}

// errFailure est l'erreur que le dépôt rend quand on lui demande de tomber. Elle
// n'est aucune des erreurs du domaine, ce qui est le point du test : une panne de
// persistance ne doit pas se déguiser en « identifiants invalides ».
var errFailure = errors.New("panne de la base")

// memRepo est un [identity.UserRepository] en mémoire. Il respecte le
// contrat du port sur les deux points que le domaine ne peut pas vérifier :
// unicité de l'email et erreurs sentinelles.
type memRepo struct {
	accounts map[identity.ID]identity.User
	// failing fait échouer toute opération, pour distinguer une panne d'un refus.
	failing bool
}

func newRepo() *memRepo {
	return &memRepo{accounts: make(map[identity.ID]identity.User)}
}

func (d *memRepo) Create(_ context.Context, user identity.User) error {
	if d.failing {
		return errFailure
	}
	for _, existing := range d.accounts {
		if existing.Email == user.Email {
			return fmt.Errorf("%w : %s", identity.ErrEmailTaken, user.Email)
		}
	}
	d.accounts[user.ID] = user
	return nil
}

func (d *memRepo) ByEmail(_ context.Context, email string) (identity.User, error) {
	if d.failing {
		return identity.User{}, errFailure
	}
	for _, user := range d.accounts {
		if user.Email == email {
			return user, nil
		}
	}
	// Enveloppée : le domaine doit reconnaître la sentinelle au travers de
	// l'enveloppe, comme le fera l'adapter PostgreSQL.
	return identity.User{}, fmt.Errorf("lecture par email : %w", identity.ErrUnknownUser)
}

func (d *memRepo) ByID(_ context.Context, id identity.ID) (identity.User, error) {
	if d.failing {
		return identity.User{}, errFailure
	}
	user, ok := d.accounts[id]
	if !ok {
		return identity.User{}, fmt.Errorf("lecture par identifiant : %w", identity.ErrUnknownUser)
	}
	return user, nil
}

func (d *memRepo) Update(_ context.Context, user identity.User) error {
	if d.failing {
		return errFailure
	}
	if _, ok := d.accounts[user.ID]; !ok {
		return fmt.Errorf("mise à jour : %w", identity.ErrUnknownUser)
	}
	d.accounts[user.ID] = user
	return nil
}

func (d *memRepo) List(_ context.Context) ([]identity.User, error) {
	if d.failing {
		return nil, errFailure
	}
	accounts := slices.Collect(maps.Values(d.accounts))
	slices.SortFunc(accounts, func(a, b identity.User) int {
		return strings.Compare(a.Email, b.Email)
	})
	return accounts, nil
}

// harness rassemble le service et ses doublures, pour que chaque test parte d'un
// état neuf sans répéter le montage.
type harness struct {
	service *identity.AccountService
	repo    *memRepo
	hasher  *plainHasher
	// instant est l'heure que rend l'horloge du service. La faire avancer se fait
	// en l'écrasant, ce qui rend les tests d'horodatage déterministes.
	instant time.Time
}

// frozenClock est l'instant de référence des tests. Une date fixe rend les
// comparaisons d'horodatage lisibles dans les messages d'échec.
var frozenClock = time.Date(2026, time.March, 15, 10, 30, 0, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		repo:    newRepo(),
		hasher:  &plainHasher{},
		instant: frozenClock,
	}

	// Les identifiants sont tirés en séquence : un test qui affiche un
	// identifiant reste lisible, et l'égalité de deux comptes se voit.
	var counter atomic.Int64

	service, err := identity.NewAccountService(identity.ServiceOptions{
		Repo:   h.repo,
		Hasher: h.hasher,
		Clock:  func() time.Time { return h.instant },
		NewID: func() (identity.ID, error) {
			return identity.ID(fmt.Sprintf("compte-%02d", counter.Add(1))), nil
		},
	})
	if err != nil {
		t.Fatalf("identity.NewAccountService() échoué : %v", err)
	}
	h.service = service

	return h
}

// createAccount ouvre un compte valide et échoue le test si ce n'est pas possible.
func (h *harness) createAccount(t *testing.T, email string, role identity.Role) identity.User {
	t.Helper()

	user, err := h.service.Create(t.Context(), email, "Compte de test", validPassword, role)
	if err != nil {
		t.Fatalf("Create(%q) échoué : %v", email, err)
	}

	return user
}

// validPassword fait bien plus que la longueur minimale, pour qu'un test qui
// change la borne n'ait pas à le retoucher.
const validPassword = "phrase de passe raisonnablement longue"

// render affiche une valeur comme le ferait un journal maladroit : c'est
// exactement le geste dont on veut vérifier qu'il ne divulgue pas d'empreinte.
func render(v any) string {
	return fmt.Sprintf("%v / %+v", v, v)
}
