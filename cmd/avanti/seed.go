package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/planning"
	"github.com/Romain-Badino/Avanti/internal/platform/config"
)

// Les sous-commandes de `avanti seed`.
const subcmdSeedDemo = "demo"

// runSeed aiguille `avanti seed …`.
//
// Le seed est un outil d'essai : il remplit une instance VIDE d'un jeu de
// données réaliste pour découvrir l'application — jamais pour préparer une
// vraie base. Les deux garde-fous de seedDemo (refus en production, refus dès
// qu'une donnée métier existe) sont là pour que la confusion soit impossible.
func runSeed(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usageSeed(stderr)
		return errors.New("commande seed : sous-commande manquante")
	}

	switch args[0] {
	case subcmdSeedDemo:
		return seedDemo(ctx, args[1:], stdout, stderr)
	default:
		usageSeed(stderr)
		return fmt.Errorf("commande seed : sous-commande inconnue %q", args[0])
	}
}

func usageSeed(out io.Writer) {
	help := &sink{w: out}
	help.printf(`Usage : avanti seed <sous-commande> [options]

Sous-commandes
  demo   Remplit une instance vide d'un jeu de données de démonstration

Options de « demo »
  --email   Adresse email du compte (existant) qui signera les saisies (obligatoire)

Le seed est un outil d'essai. Il refuse de tourner quand AVANTI_ENV vaut
production, et dès que la base contient la moindre donnée métier : il remplit
une instance vide, il ne complète jamais une instance vécue.
`)
	// L'aide est le dernier recours d'une commande qui a déjà échoué : si même
	// elle ne s'écrit pas, il n'y a plus rien à en dire.
}

// seedDemo crée le jeu de démonstration : consultations d'artisans avec un
// devis retenu, factures et acomptes à tous les stades du suivi assurance,
// étapes de chantier avec dépendances, jalons, et pièces jointes PDF.
//
// Tout passe par les SERVICES de domaine, jamais par du SQL : le seed vit dans
// cmd/avanti, seul endroit du dépôt autorisé à assembler les domaines (R4 de
// docs/ARCHITECTURE.md), et ce qu'il crée respecte donc les mêmes invariants
// que des saisies réelles.
func seedDemo(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("avanti seed demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usageSeed(stderr) }

	email := flags.String("email", "", "adresse email du compte qui signera les saisies")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("analyse des arguments : %w", err)
	}
	if *email == "" {
		usageSeed(stderr)
		return errors.New("seed demo : --email est obligatoire")
	}

	// Le refus de la production se décide sur la seule configuration, AVANT
	// d'ouvrir la base : pas question d'appliquer des migrations à une instance
	// réelle pour découvrir ensuite qu'on n'avait rien à y faire.
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	if cfg.Environment == config.Production {
		return errors.New("seed demo : refusé quand AVANTI_ENV vaut production — le seed est un outil d'essai, pas un outil d'exploitation")
	}

	app, cleanup, err := openInstance(ctx, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	// Le compte doit exister : le seed n'invente pas d'identité, il attribue
	// ses saisies à quelqu'un que `avanti user add` a créé.
	user, err := app.accounts.ByEmail(ctx, *email)
	if err != nil {
		return fmt.Errorf("seed demo : compte %q introuvable — créez-le d'abord avec « avanti user add » : %w", *email, err)
	}

	// Une seule demande de devis suffit à dire que la base a vécu : le seed
	// remplit une instance vide, il ne se mélange jamais à de vraies données.
	demandes, err := app.devisService.Demandes(ctx)
	if err != nil {
		return err
	}
	if len(demandes) > 0 {
		return fmt.Errorf("seed demo : la base contient déjà %d demande(s) de devis — le seed ne tourne que sur une instance vide", len(demandes))
	}

	out := &sink{w: stdout}
	if seedErr := buildDemoData(ctx, app, user.ID.String(), out); seedErr != nil {
		return fmt.Errorf("seed demo : %w", seedErr)
	}
	if out.err != nil {
		return out.err
	}

	out.printf("\nJeu de démonstration créé. Connectez-vous avec le compte %s pour le parcourir.\n", user.Email)
	return out.err
}

// demoSeed porte le contexte commun aux étapes du seed : l'instance, l'acteur
// qui signe, l'horloge de référence et la sortie.
type demoSeed struct {
	app *instance
	by  string
	now time.Time
	out *sink
}

// buildDemoData crée le jeu complet, domaine par domaine. Les dates sont
// relatives au jour d'exécution pour que les statuts dérivés (étape en retard,
// jalon futur) racontent la même histoire quel que soit le jour où l'on essaie.
func buildDemoData(ctx context.Context, app *instance, actorID string, out *sink) error {
	seed := &demoSeed{app: app, by: actorID, now: time.Now().UTC(), out: out}

	retained, err := seed.devis(ctx)
	if err != nil {
		return err
	}

	factureCharpente, err := seed.finance(ctx, retained)
	if err != nil {
		return err
	}

	if err := seed.planning(ctx, retained); err != nil {
		return err
	}

	return seed.documents(ctx, retained, factureCharpente)
}

// daysAgo rend un jour à la date du seed moins n jours ; négatif pour un jour
// futur.
func (s *demoSeed) daysAgo(n int) time.Time {
	return s.now.AddDate(0, 0, -n).Truncate(24 * time.Hour)
}

// devis crée trois consultations à trois stades : tranchée, en comparaison,
// en attente d'offres. Rend le devis retenu, que finance et planning vont
// référencer par identifiant faible (R2).
func (s *demoSeed) devis(ctx context.Context) (devis.Devis, error) {
	by := devis.ActeurID(s.by)
	svc := s.app.devisService

	charpente, err := svc.CreateDemande(ctx, devis.DemandeInput{
		Lot:         "Charpente et couverture",
		Description: "Reprise complète de la charpente sinistrée et couverture en tuiles plates, zinguerie comprise.",
		Artisans: []devis.Artisan{
			{Entreprise: "Charpentes Morel", Email: "contact@charpentes-morel.example"},
			{Entreprise: "Toitures Vasseur", Telephone: "02 00 00 00 01"},
		},
		SentAt: s.daysAgo(120),
		By:     by,
	})
	if err != nil {
		return devis.Devis{}, err
	}

	morel, err := svc.RecordDevis(ctx, devis.DevisInput{
		DemandeID:  charpente.ID,
		Artisan:    devis.Artisan{Entreprise: "Charpentes Morel", Email: "contact@charpentes-morel.example"},
		Montant:    4_835_000, // 48 350,00 €
		ReceivedAt: s.daysAgo(100),
		Validity:   90 * 24 * time.Hour,
		Notes:      "Bois traité classe 2, échafaudage compris.",
		By:         by,
	})
	if err != nil {
		return devis.Devis{}, err
	}
	if _, err = svc.RecordDevis(ctx, devis.DevisInput{
		DemandeID:  charpente.ID,
		Artisan:    devis.Artisan{Entreprise: "Toitures Vasseur", Telephone: "02 00 00 00 01"},
		Montant:    5_290_000, // 52 900,00 €
		ReceivedAt: s.daysAgo(95),
		By:         by,
	}); err != nil {
		return devis.Devis{}, err
	}

	// Retenir Morel refuse Vasseur dans la même décision : la comparaison est
	// close, comme sur un vrai chantier.
	retained, err := svc.Retain(ctx, morel.ID, by)
	if err != nil {
		return devis.Devis{}, err
	}

	electricite, err := svc.CreateDemande(ctx, devis.DemandeInput{
		Lot:         "Électricité — mise aux normes",
		Description: "Tableau neuf, réseau complet et mise à la terre après expertise.",
		Artisans: []devis.Artisan{
			{Entreprise: "Élec Chantier SARL"},
			{Entreprise: "Éts Lambert et Fils"},
		},
		SentAt: s.daysAgo(30),
		By:     by,
	})
	if err != nil {
		return devis.Devis{}, err
	}
	if _, err = svc.RecordDevis(ctx, devis.DevisInput{
		DemandeID:  electricite.ID,
		Artisan:    devis.Artisan{Entreprise: "Élec Chantier SARL"},
		Montant:    1_248_050, // 12 480,50 €
		ReceivedAt: s.daysAgo(15),
		By:         by,
	}); err != nil {
		return devis.Devis{}, err
	}
	if _, err = svc.RecordDevis(ctx, devis.DevisInput{
		DemandeID:  electricite.ID,
		Artisan:    devis.Artisan{Entreprise: "Éts Lambert et Fils"},
		Montant:    1_190_000, // 11 900,00 €
		ReceivedAt: s.daysAgo(12),
		Notes:      "Hors reprise de la VMC.",
		By:         by,
	}); err != nil {
		return devis.Devis{}, err
	}

	if _, err = svc.CreateDemande(ctx, devis.DemandeInput{
		Lot:         "Plâtrerie et isolation",
		Description: "Doublage des murs périphériques et plafonds de l'étage.",
		Artisans:    []devis.Artisan{{Entreprise: "Plâtrerie Bodin"}},
		SentAt:      s.daysAgo(7),
		By:          by,
	}); err != nil {
		return devis.Devis{}, err
	}

	s.out.printf("Devis : 3 demandes créées — « Charpente et couverture » tranchée (devis Charpentes Morel retenu,\n")
	s.out.printf("        Toitures Vasseur refusé), « Électricité » en comparaison (2 offres), « Plâtrerie » en attente.\n")

	return retained, nil
}

// finance crée trois factures aux trois stades du suivi assurance et un
// acompte sous le montant engagé du devis retenu. Rend la facture rattachée au
// devis, cible d'une pièce jointe.
func (s *demoSeed) finance(ctx context.Context, retained devis.Devis) (finance.Facture, error) {
	by := finance.ActeurID(s.by)
	svc := s.app.financeService

	charpente, err := svc.RecordFacture(ctx, finance.FactureInput{
		DevisID:    retained.ID.String(),
		Entreprise: "Charpentes Morel",
		Montant:    2_417_500, // 24 175,00 € : situation n° 1, moitié du marché
		Date:       s.daysAgo(20),
		Numero:     "2026-041",
		Notes:      "Situation n° 1 — charpente posée.",
		By:         by,
	})
	if err != nil {
		return finance.Facture{}, err
	}
	if charpente, err = svc.MarkFacturePayee(ctx, charpente.ID, by); err != nil {
		return finance.Facture{}, err
	}

	expertise, err := svc.RecordFacture(ctx, finance.FactureInput{
		Entreprise: "Cabinet Perrin Expertise",
		Montant:    185_000, // 1 850,00 €
		Date:       s.daysAgo(60),
		Numero:     "EXP-2026-197",
		Notes:      "Rapport de structure après sinistre.",
		By:         by,
	})
	if err != nil {
		return finance.Facture{}, err
	}
	if _, err = svc.MarkFactureEnvoyeeAssurance(ctx, expertise.ID, by); err != nil {
		return finance.Facture{}, err
	}

	bennes, err := svc.RecordFacture(ctx, finance.FactureInput{
		Entreprise: "Location Bennes Service",
		Montant:    72_000, // 720,00 €
		Date:       s.daysAgo(90),
		Notes:      "Évacuation des gravats de démolition.",
		By:         by,
	})
	if err != nil {
		return finance.Facture{}, err
	}
	if _, err = svc.MarkFactureEnvoyeeAssurance(ctx, bennes.ID, by); err != nil {
		return finance.Facture{}, err
	}
	if _, err = svc.MarkFactureRemboursee(ctx, bennes.ID, 72_000, by); err != nil {
		return finance.Facture{}, err
	}

	// L'acompte reste sous le montant engagé : l'invariant central du domaine
	// est vérifié par le service PUIS par la base, comme pour une vraie saisie.
	if _, err = svc.RecordAcompte(ctx, finance.AcompteInput{
		DevisID:       retained.ID.String(),
		Entreprise:    "Charpentes Morel",
		Montant:       1_450_500, // 14 505,00 € : 30 % à la commande
		Date:          s.daysAgo(40),
		Moyen:         finance.MoyenVirement,
		Notes:         "Acompte de 30 % à la commande.",
		MontantEngage: finance.Montant(retained.Montant),
		By:            by,
	}); err != nil {
		return finance.Facture{}, err
	}

	s.out.printf("Finances : 3 factures (une payée, une envoyée à l'assurance, une remboursée)\n")
	s.out.printf("        et 1 acompte de 30 %% sous le montant engagé du devis retenu.\n")

	return charpente, nil
}

// planning crée quatre étapes à quatre statuts dérivés — terminée, en cours,
// en retard, prête à démarrer — et deux jalons, un atteint et un futur.
func (s *demoSeed) planning(ctx context.Context, retained devis.Devis) error {
	by := planning.ActeurID(s.by)
	svc := s.app.planningService

	demolition, err := svc.CreateEtape(ctx, planning.EtapeInput{
		Name:         "Démolition et curage",
		Description:  "Dépose des éléments sinistrés et évacuation.",
		PlannedStart: s.daysAgo(80),
		PlannedEnd:   s.daysAgo(50),
		By:           by,
	})
	if err != nil {
		return err
	}
	if startErr := s.startEtape(ctx, demolition.ID); startErr != nil {
		return startErr
	}
	if finishErr := s.finishEtape(ctx, demolition.ID); finishErr != nil {
		return finishErr
	}

	charpente, err := svc.CreateEtape(ctx, planning.EtapeInput{
		Name:         "Charpente",
		Description:  "Reprise de la charpente par Charpentes Morel.",
		PlannedStart: s.daysAgo(30),
		PlannedEnd:   s.daysAgo(-20),
		DependsOn:    []planning.ID{demolition.ID},
		DevisID:      retained.ID.String(),
		By:           by,
	})
	if err != nil {
		return err
	}
	if startErr := s.startEtape(ctx, charpente.ID); startErr != nil {
		return startErr
	}

	// La couverture dépend de la charpente, encore en cours : son début prévu
	// est passé, elle s'affiche donc en retard — c'est le statut dérivé des
	// dates, jamais une donnée stockée.
	if _, err = svc.CreateEtape(ctx, planning.EtapeInput{
		Name:         "Couverture",
		Description:  "Tuiles plates et zinguerie, à la suite de la charpente.",
		PlannedStart: s.daysAgo(10),
		PlannedEnd:   s.daysAgo(-15),
		DependsOn:    []planning.ID{charpente.ID},
		DevisID:      retained.ID.String(),
		By:           by,
	}); err != nil {
		return err
	}

	// L'électricité ne dépend que de la démolition, terminée : tous ses
	// prérequis sont levés, elle apparaît « prête à démarrer ».
	if _, err = svc.CreateEtape(ctx, planning.EtapeInput{
		Name:         "Électricité",
		Description:  "Mise aux normes complète, en parallèle de la couverture.",
		PlannedStart: s.daysAgo(-10),
		PlannedEnd:   s.daysAgo(-40),
		DependsOn:    []planning.ID{demolition.ID},
		By:           by,
	}); err != nil {
		return err
	}

	finDemolition, err := svc.CreateJalon(ctx, planning.JalonInput{
		Name: "Fin de la démolition",
		Date: s.daysAgo(45),
		By:   by,
	})
	if err != nil {
		return err
	}
	// L'horodatage attendu par la garde optimiste vient d'une RELECTURE : la
	// base stocke les dates en microsecondes, la valeur rendue à la création
	// n'y est jamais repassée (voir le contrat de planning.Repository).
	stored, err := svc.Jalon(ctx, finDemolition.ID)
	if err != nil {
		return err
	}
	if _, err = svc.ReachJalon(ctx, finDemolition.ID, stored.UpdatedAt, by); err != nil {
		return err
	}

	if _, err = svc.CreateJalon(ctx, planning.JalonInput{
		Name: "Clos et couvert",
		Date: s.daysAgo(-30),
		By:   by,
	}); err != nil {
		return err
	}

	s.out.printf("Planning : 4 étapes (démolition terminée, charpente en cours, couverture en retard,\n")
	s.out.printf("        électricité prête à démarrer) et 2 jalons (un atteint, un futur).\n")

	return nil
}

// startEtape relit l'étape puis la démarre : la garde optimiste exige
// l'horodatage tel que la base l'a stocké, pas celui que la création a rendu.
func (s *demoSeed) startEtape(ctx context.Context, id planning.ID) error {
	stored, err := s.app.planningService.Etape(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.app.planningService.StartEtape(ctx, id, stored.UpdatedAt, planning.ActeurID(s.by))
	return err
}

// finishEtape relit l'étape puis la termine, pour la même raison que
// [demoSeed.startEtape].
func (s *demoSeed) finishEtape(ctx context.Context, id planning.ID) error {
	stored, err := s.app.planningService.Etape(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.app.planningService.FinishEtape(ctx, id, stored.UpdatedAt, planning.ActeurID(s.by))
	return err
}

// documents dépose deux petits PDF de démonstration, engendrés en mémoire :
// le devis signé rattaché au devis retenu, la facture scannée rattachée à la
// facture de situation. Le rattachement est un couple (type, identifiant) —
// la référence faible de R2.
func (s *demoSeed) documents(ctx context.Context, retained devis.Devis, facture finance.Facture) error {
	svc := s.app.documentsService
	by := document.ActeurID(s.by)

	devisPDF := demoPDF("Avanti - devis de demonstration - Charpentes Morel")
	if _, err := svc.Upload(ctx, document.UploadInput{
		FileName:    "devis-charpente-morel-signe.pdf",
		MimeType:    "application/pdf",
		SizeBytes:   int64(len(devisPDF)),
		Content:     bytes.NewReader(devisPDF),
		Category:    document.CategoryDevisSigne.String(),
		Description: "Devis signé de Charpentes Morel (démonstration).",
		Target:      document.Target{Type: document.TargetDevis, ID: retained.ID.String()},
		By:          by,
	}); err != nil {
		return err
	}

	facturePDF := demoPDF("Avanti - facture de demonstration - situation n 1")
	if _, err := svc.Upload(ctx, document.UploadInput{
		FileName:    "facture-charpente-situation-1.pdf",
		MimeType:    "application/pdf",
		SizeBytes:   int64(len(facturePDF)),
		Content:     bytes.NewReader(facturePDF),
		Category:    document.CategoryFacture.String(),
		Description: "Facture de situation n° 1 (démonstration).",
		Target:      document.Target{Type: document.TargetFacture, ID: facture.ID.String()},
		By:          by,
	}); err != nil {
		return err
	}

	s.out.printf("Documents : 2 PDF de démonstration rattachés au devis retenu et à la facture payée.\n")

	return nil
}

// demoPDF engendre en mémoire un PDF minimal d'une page portant le libellé
// donné — environ un kilooctet, contenu neutre. Le libellé doit rester en
// ASCII sans parenthèses : il entre tel quel dans une chaîne littérale PDF.
func demoPDF(label string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 72 760 Td (%s) Tj ET", label)

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// Les offsets de la table xref se mesurent au fil de l'écriture : c'est ce
	// qui rend le fichier valide pour un lecteur strict, pas seulement pour un
	// lecteur indulgent.
	offsets := make([]int, 0, len(objects))
	for i, obj := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	return buf.Bytes()
}
