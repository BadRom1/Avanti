package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ory/fosite/storage"
)

// L'interface facultative de transaction de fosite, vérifiée à la compilation.
//
// Elle est facultative au sens de fosite — [storage.MaybeBeginTx] et ses
// jumelles ne font rien quand le magasin ne l'implémente pas — et c'est
// précisément ce qui rend l'assertion nécessaire : une méthode dont la signature
// dériverait ne provoquerait aucune erreur, elle ferait *silencieusement*
// retomber le serveur d'autorisation dans le mode sans transaction. La rotation
// des jetons de rafraîchissement cesserait alors d'être atomique sans qu'aucun
// test du protocole ne s'en aperçoive.
var _ storage.Transactional = (*OAuthStore)(nil)

// oauthTxKey désigne la transaction en cours dans un contexte.
//
// Le type est privé et sans champ : aucun autre paquet ne peut fabriquer cette
// clé, donc aucun autre paquet ne peut glisser une fausse transaction dans un
// contexte que ce magasin lira.
type oauthTxKey struct{}

// querier est le dénominateur commun du pool et d'une transaction pgx.
//
// Il ne déclare que les deux méthodes que ce magasin emploie. En déclarer
// davantage obligerait à les couvrir des deux côtés sans rien apporter.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// q rend le querier à employer : la transaction que le contexte porte, ou le
// pool.
//
// Toutes les méthodes du magasin passent par lui, sans exception. C'est ce qui
// fait qu'une écriture reste dans la transaction ouverte par fosite au lieu de
// partir sur une connexion voisine — auquel cas elle serait visible avant le
// commit, et ne serait pas défaite par un rollback.
func (s *OAuthStore) q(ctx context.Context) querier {
	if tx, ok := ctx.Value(oauthTxKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.pool
}

// BeginTX ouvre une transaction et la range dans le contexte rendu.
//
// # Le contexte comme véhicule, et le risque qui va avec
//
// fosite impose ce mode d'emploi : il appelle BeginTX, poursuit la séquence avec
// le contexte rendu, puis appelle Commit ou Rollback avec ce même contexte. La
// transaction voyage donc dans un context.Context, ce qui a un coût connu — un
// contexte égaré ne rendrait jamais sa connexion au pool, et une instance qui
// n'en a que quelques-unes finirait par ne plus servir.
//
// Ce coût est accepté parce que la contrepartie est la seule chose qui rende la
// rotation des jetons de rafraîchissement sûre : sans transaction, fosite
// désactive l'ancien jeton puis crée le nouveau en deux écritures indépendantes,
// et deux requêtes concurrentes peuvent toutes deux passer entre les deux. Voir
// [OAuthStore.RotateRefreshToken].
//
// Le garde-fou contre l'égarement est ici : une transaction déjà présente dans
// le contexte est une erreur, jamais une réutilisation silencieuse. fosite
// n'imbrique pas ses transactions ; si une version future le faisait, l'échec
// serait visible à la première requête plutôt que dissimulé dans un commit
// prématuré.
func (s *OAuthStore) BeginTX(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(oauthTxKey{}).(pgx.Tx); ok {
		return ctx, errors.New("postgres : une transaction OAuth est déjà ouverte dans ce contexte")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ctx, fmt.Errorf("ouverture de la transaction OAuth : %w", err)
	}

	return context.WithValue(ctx, oauthTxKey{}, tx), nil
}

// Commit valide la transaction que le contexte porte.
func (s *OAuthStore) Commit(ctx context.Context) error {
	tx, ok := ctx.Value(oauthTxKey{}).(pgx.Tx)
	if !ok {
		return errors.New("postgres : validation d'une transaction OAuth qui n'a pas été ouverte")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation de la transaction OAuth : %w", err)
	}

	return nil
}

// Rollback défait la transaction que le contexte porte.
//
// Une transaction déjà refermée n'est pas une erreur : fosite annule dans un
// defer, y compris sur le chemin où le commit vient d'échouer — et un commit
// raté referme déjà la transaction côté pgx. Rendre une erreur là remplacerait,
// dans le journal comme dans la réponse, la cause réelle par le symptôme.
func (s *OAuthStore) Rollback(ctx context.Context) error {
	tx, ok := ctx.Value(oauthTxKey{}).(pgx.Tx)
	if !ok {
		return errors.New("postgres : annulation d'une transaction OAuth qui n'a pas été ouverte")
	}

	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("annulation de la transaction OAuth : %w", err)
	}

	return nil
}
