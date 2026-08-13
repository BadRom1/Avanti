-- Le second invariant de la consultation, celui que la migration 00006 avait
-- laissé au seul code : une demande dont un devis est retenu n'accepte plus de
-- devis en attente. « Un seul retenu par demande » était déjà tenu par un index
-- unique partiel ; « une demande tranchée est close » ne l'était par rien.
--
-- --- Pourquoi un trigger et pas une contrainte
--
-- La règle porte sur une ligne en fonction de *ses sœurs* : elle regarde les
-- autres devis de la même demande. Ni CHECK — qui ne voit que la ligne écrite —
-- ni index unique — qui ne sait qu'interdire un doublon — ne l'expriment. Reste
-- le trigger, qui est ici le seul moyen de dire l'invariant à la base plutôt
-- qu'à la discipline de l'appelant.
--
-- --- Pourquoi le verrou sur la demande, et pourquoi il ne se retire pas
--
-- Sans lui, le contrôle serait exactement le « lire puis écrire » qu'il vient
-- remplacer, déplacé de trois centimètres. En READ COMMITTED, une rétention
-- concurrente non validée est invisible : le trigger lirait « aucun retenu »,
-- laisserait passer l'insertion, et le devis reçu atterrirait sur une demande
-- déjà tranchée — l'écran afficherait une comparaison close portant une offre
-- qui n'a jamais été en jeu.
--
-- Le verrou pris sur la ligne de la demande sérialise les deux chemins, parce
-- que la rétention prend le même (voir DevisRepo.Retain). Deux ordres sont alors
-- possibles, et les deux sont bons : ou l'insertion passe la première et la
-- rétention refuse ensuite ce devis avec ses concurrents, ou la rétention passe
-- la première et le trigger refuse l'insertion. Aucune fenêtre entre les deux.
--
-- Le verrou est pris sur *la demande* et non sur les devis : c'est la seule
-- ligne dont on sait qu'elle existe avant qu'un devis n'arrive, donc le seul
-- point de rendez-vous que les deux transactions puissent se donner. Quand la
-- demande n'existe pas, il n'y a rien à verrouiller et rien à dire : la clé
-- étrangère refusera l'insertion avec son propre message.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION devis_refuse_si_demande_close() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- Point de rendez-vous avec la rétention concurrente. Le résultat n'a aucun
    -- intérêt : c'est le verrou qu'on vient chercher.
    PERFORM 1 FROM demandes_devis WHERE id = NEW.demande_id FOR UPDATE;

    IF EXISTS (
        SELECT 1 FROM devis WHERE demande_id = NEW.demande_id AND statut = 'retenu'
    ) THEN
        -- Le nom de contrainte est ce que l'adapter reconnaît, pas le message :
        -- une phrase se reformule, un identifiant se cherche. Il porte le nom du
        -- trigger, de sorte que l'erreur désigne le garde-fou qui l'a levée.
        RAISE EXCEPTION 'la demande % porte déjà un devis retenu', NEW.demande_id
            USING ERRCODE = 'P0001', CONSTRAINT = 'devis_demande_ouverte';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Le trigger ne se déclenche que sur un devis « recu ». C'est le seul statut
-- qu'une saisie produit — un devis naît reçu — et donc le seul dont l'arrivée
-- rouvrirait une comparaison close. Un devis inséré directement en « retenu »
-- reste l'affaire de l'index unique partiel, un « refuse » ne dit rien de
-- l'ouverture de la demande : c'est même l'état où la rétention laisse les
-- concurrents.
CREATE TRIGGER devis_demande_ouverte
    BEFORE INSERT ON devis
    FOR EACH ROW
    WHEN (NEW.statut = 'recu')
    EXECUTE FUNCTION devis_refuse_si_demande_close();

COMMENT ON FUNCTION devis_refuse_si_demande_close() IS
    'Refuse un devis reçu sur une demande dont un devis est déjà retenu. Verrouille la ligne de la demande pour se sérialiser avec la rétention concurrente.';

-- +goose Down
DROP TRIGGER devis_demande_ouverte ON devis;
DROP FUNCTION devis_refuse_si_demande_close();
