// Package debt implements the Debt Rescue Mode planner: a deterministic
// amortization engine that turns a household's income, expenses and debts into
// a concrete recovery plan.
//
// Every figure this package reports (payoff order, debt-free date, interest
// saved) is computed by simulating the loans month by month. Nothing here is
// estimated or model-generated — the AI layer consumes these numbers as ground
// truth and only writes prose around them.
package debt

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxMonths caps every simulation so a debt that never amortizes cannot spin
// forever. 600 months (50 years) is far beyond any realistic payoff plan.
const maxMonths = 600

// bufferRate holds back a slice of the surplus above minimum payments so the
// plan is not knife-edge — one unexpected expense shouldn't break it.
const bufferRate = 0.10

// epsilon is the sub-cent threshold below which a balance counts as cleared.
// Float accumulation across hundreds of months leaves dust that would
// otherwise keep a paid-off loan alive forever.
const epsilon = 0.005

// nbsp is U+00A0 NO-BREAK SPACE — the exact separator Intl places between a
// multi-character currency symbol and the digits ("RM" + nbsp + "6,250").
// Named rather than inlined so the invisible character lives in one place.
const nbsp = " "

// Strategy names for the repayment ordering.
const (
	Avalanche = "avalanche" // highest interest rate first — cheapest overall
	Snowball  = "snowball"  // smallest balance first — fastest first win
)

// Debt is one outstanding obligation.
type Debt struct {
	Name       string  `json:"name"`
	Balance    float64 `json:"balance"`
	APR        float64 `json:"apr"` // annual rate as a percent, e.g. 19.99
	MinPayment float64 `json:"min_payment"`
}

// Input is the household's full financial picture.
type Input struct {
	MonthlyIncome float64 `json:"monthly_income"`
	FixedExpenses float64 `json:"fixed_expenses"`
	AvailableCash float64 `json:"available_cash"`
	Debts         []Debt  `json:"debts"`
	// Strategy selects the repayment ordering; defaults to Avalanche.
	Strategy string `json:"strategy"`
	// PaymentOverride, when > 0, replaces the computed monthly target — for
	// users who know what they can actually afford.
	PaymentOverride float64 `json:"payment_override"`
	// Currency is an ISO code (USD, MYR, …) used for the symbol in the prose
	// this engine writes. It is a display concern only — no amount is ever
	// converted. Empty falls back to USD.
	Currency string `json:"currency"`
	// StartDate anchors the debt-free date. Zero means "now"; tests pin it.
	StartDate time.Time `json:"-"`
}

// SurvivalBudget is the emergency month-one budget: where every ringgit of
// income is going before any accelerated payoff.
type SurvivalBudget struct {
	MonthlyIncome    float64 `json:"monthly_income"`
	FixedExpenses    float64 `json:"fixed_expenses"`
	MinimumPayments  float64 `json:"minimum_payments"`
	Essentials       float64 `json:"essentials"`
	Disposable       float64 `json:"disposable"`
	SafetyBuffer     float64 `json:"safety_buffer"`
	FreeToAllocate   float64 `json:"free_to_allocate"`
	EmergencyReserve float64 `json:"emergency_reserve"`
	LumpSumDeployed  float64 `json:"lump_sum_deployed"`
	// Shortfall is how far income falls short of essentials. > 0 means the
	// household cannot cover minimums and the plan is not survivable as-is.
	Shortfall float64 `json:"shortfall"`
}

// OrderItem is one debt's position in the repayment queue with its outcome.
type OrderItem struct {
	Position     int     `json:"position"`
	Name         string  `json:"name"`
	Balance      float64 `json:"balance"`
	APR          float64 `json:"apr"`
	MinPayment   float64 `json:"min_payment"`
	MonthlyCost  float64 `json:"monthly_cost"` // interest accruing per month today
	PayoffMonth  int     `json:"payoff_month"`
	PayoffDate   string  `json:"payoff_date"`
	NeverPaysOff bool    `json:"never_pays_off"`
}

// MonthRow is one month of the payoff schedule, for charting progress.
type MonthRow struct {
	Month     int     `json:"month"`
	Date      string  `json:"date"`
	Balance   float64 `json:"balance"`
	Interest  float64 `json:"interest"`
	Principal float64 `json:"principal"`
}

// StrategySummary lets the UI show avalanche vs snowball side by side.
type StrategySummary struct {
	Strategy      string  `json:"strategy"`
	Months        int     `json:"months"`
	TotalInterest float64 `json:"total_interest"`
	DebtFreeDate  string  `json:"debt_free_date"`
	Converged     bool    `json:"converged"`
}

// Plan is the complete recovery plan.
type Plan struct {
	Feasible bool   `json:"feasible"`
	Strategy string `json:"strategy"`
	// Currency echoes the code the prose in Summary/Warnings was written with,
	// so the UI can format its own figures to match.
	Currency string         `json:"currency"`
	Survival SurvivalBudget `json:"survival"`

	TotalDebt            float64 `json:"total_debt"`
	MonthlyPaymentTarget float64 `json:"monthly_payment_target"`

	Order []OrderItem `json:"order"`

	MonthsToFreedom int    `json:"months_to_freedom"`
	DebtFreeDate    string `json:"debt_free_date"`

	TotalInterest    float64 `json:"total_interest"`
	BaselineInterest float64 `json:"baseline_interest"`
	BaselineMonths   int     `json:"baseline_months"`
	BaselineCapped   bool    `json:"baseline_capped"`
	InterestSaved    float64 `json:"interest_saved"`
	MonthsSaved      int     `json:"months_saved"`

	Comparison []StrategySummary `json:"comparison"`
	Schedule   []MonthRow        `json:"schedule"`

	Warnings []string `json:"warnings"`
	// Summary is a one-line plain-language headline, matching the shape of
	// the example in the product spec.
	Summary string `json:"summary"`
}

// ensureSlices replaces nil slices with empty ones.
//
// A nil Go slice marshals to JSON `null`, not `[]`. The browser then reads
// `plan.warnings.length` off null and the whole panel crashes — which is
// exactly what happened, because a healthy plan has no warnings. Every
// consumer is entitled to an array here, so guarantee it at the boundary.
func (p *Plan) ensureSlices() {
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	if p.Order == nil {
		p.Order = []OrderItem{}
	}
	if p.Comparison == nil {
		p.Comparison = []StrategySummary{}
	}
	if p.Schedule == nil {
		p.Schedule = []MonthRow{}
	}
}

// Build runs the full planner. It never returns an error: an unaffordable
// situation is a valid — and important — result, reported via Feasible=false
// with the shortfall and warnings explaining what has to change.
func Build(in Input) (plan Plan) {
	// Every return path must hand back arrays, never nils — see ensureSlices.
	defer func() { plan.ensureSlices() }()
	return build(in)
}

func build(in Input) Plan {
	start := in.StartDate
	if start.IsZero() {
		start = time.Now()
	}
	cf := formatFor(in.Currency)

	// Keep only real debts; a zero balance is already won.
	debts := make([]Debt, 0, len(in.Debts))
	for _, d := range in.Debts {
		if d.Balance > epsilon {
			if d.APR < 0 {
				d.APR = 0
			}
			if d.MinPayment < 0 {
				d.MinPayment = 0
			}
			debts = append(debts, d)
		}
	}

	var totalDebt, totalMinimums float64
	for _, d := range debts {
		totalDebt += d.Balance
		totalMinimums += d.MinPayment
	}

	essentials := in.FixedExpenses + totalMinimums
	disposable := in.MonthlyIncome - in.FixedExpenses

	// Hold one month of essentials as a starter emergency fund; only cash
	// beyond that gets thrown at the debt. Draining the last ringgit into a
	// loan is how people end up back on a credit card next month.
	reserve := math.Min(math.Max(in.AvailableCash, 0), essentials)
	lumpSum := math.Max(0, in.AvailableCash-reserve)

	surplus := disposable - totalMinimums
	var buffer float64
	if surplus > 0 {
		buffer = surplus * bufferRate
	}

	target := disposable - buffer
	if in.PaymentOverride > 0 {
		target = in.PaymentOverride
		buffer = math.Max(0, disposable-target)
	}

	survival := SurvivalBudget{
		MonthlyIncome:    round2(in.MonthlyIncome),
		FixedExpenses:    round2(in.FixedExpenses),
		MinimumPayments:  round2(totalMinimums),
		Essentials:       round2(essentials),
		Disposable:       round2(disposable),
		SafetyBuffer:     round2(buffer),
		FreeToAllocate:   round2(math.Max(0, target-totalMinimums)),
		EmergencyReserve: round2(reserve),
		LumpSumDeployed:  round2(lumpSum),
		Shortfall:        round2(math.Max(0, essentials-in.MonthlyIncome)),
	}

	plan := Plan{
		Strategy:             normalizeStrategy(in.Strategy),
		Currency:             strings.ToUpper(strings.TrimSpace(in.Currency)),
		Survival:             survival,
		TotalDebt:            round2(totalDebt),
		MonthlyPaymentTarget: round2(math.Max(0, target)),
	}
	if plan.Currency == "" {
		plan.Currency = "USD"
	}

	if len(debts) == 0 {
		plan.Feasible = true
		plan.Summary = "You have no outstanding debt. Redirect the surplus into savings."
		return plan
	}

	// Flag loans whose minimum cannot even cover the monthly interest — those
	// balances grow no matter how faithfully they are paid.
	for _, d := range debts {
		monthly := d.Balance * d.APR / 100 / 12
		if d.MinPayment <= monthly+epsilon && monthly > 0 {
			plan.Warnings = append(plan.Warnings,
				d.Name+": the minimum payment doesn't cover its monthly interest, so the balance grows until you pay more than "+money(monthly, cf)+"/month.")
		}
	}

	if survival.Shortfall > 0 {
		plan.Feasible = false
		plan.Warnings = append(plan.Warnings,
			"Income doesn't cover fixed expenses plus minimum payments — a "+money(survival.Shortfall, cf)+"/month gap. Before any payoff plan works, you need to cut fixed costs, raise income, or renegotiate the minimums (hardship plans, consolidation, or a lower-rate transfer).")
		plan.Summary = "You're short " + money(survival.Shortfall, cf) + "/month against essentials. The priority is closing that gap, not accelerating payoff."
		plan.Order = buildOrder(debts, orderFor(debts, plan.Strategy), map[int]int{}, start)
		return plan
	}

	if target < totalMinimums-epsilon {
		plan.Warnings = append(plan.Warnings,
			"The monthly payment target of "+money(target, cf)+" is below the "+money(totalMinimums, cf)+" of combined minimum payments.")
	}

	order := orderFor(debts, plan.Strategy)
	run := simulate(debts, order, target, lumpSum, start)

	plan.Feasible = run.converged
	plan.MonthsToFreedom = run.months
	plan.TotalInterest = round2(run.totalInterest)
	plan.Schedule = run.schedule
	plan.Order = buildOrder(debts, order, run.payoffMonth, start)

	if run.converged {
		plan.DebtFreeDate = start.AddDate(0, run.months, 0).Format("2006-01")
	} else {
		plan.Warnings = append(plan.Warnings,
			"At "+money(target, cf)+"/month these balances never clear — the interest outruns the payment. Increase the monthly amount or reduce the rates.")
	}

	// Baseline: minimums only, no rollover. This is what happens if nothing
	// changes, and it's what "interest saved" is measured against.
	base := simulateMinimumsOnly(debts)
	plan.BaselineInterest = round2(base.totalInterest)
	plan.BaselineMonths = base.months
	plan.BaselineCapped = !base.converged
	if run.converged {
		plan.InterestSaved = round2(math.Max(0, base.totalInterest-run.totalInterest))
		plan.MonthsSaved = maxInt(0, base.months-run.months)
	}

	// Both orderings, so the UI can show the real cost of choosing motivation
	// (snowball) over arithmetic (avalanche).
	for _, s := range []string{Avalanche, Snowball} {
		r := simulate(debts, orderFor(debts, s), target, lumpSum, start)
		sum := StrategySummary{
			Strategy:      s,
			Months:        r.months,
			TotalInterest: round2(r.totalInterest),
			Converged:     r.converged,
		}
		if r.converged {
			sum.DebtFreeDate = start.AddDate(0, r.months, 0).Format("2006-01")
		}
		plan.Comparison = append(plan.Comparison, sum)
	}

	plan.Summary = summarize(plan, cf)
	return plan
}

// ── simulation ────────────────────────────────────────────────────────────

type runResult struct {
	months        int
	totalInterest float64
	payoffMonth   map[int]int
	schedule      []MonthRow
	converged     bool
}

// simulate runs the avalanche/snowball payoff month by month: interest accrues,
// every debt gets its minimum, and everything left over attacks the focus debt.
// As debts clear, their freed-up minimums roll into the focus automatically.
func simulate(debts []Debt, order []int, monthlyPayment, lumpSum float64, start time.Time) runResult {
	bal := make([]float64, len(debts))
	for i, d := range debts {
		bal[i] = d.Balance
	}
	res := runResult{payoffMonth: map[int]int{}}

	// The starter lump sum lands before any interest accrues.
	rest := lumpSum
	for _, i := range order {
		if rest <= epsilon {
			break
		}
		pay := math.Min(rest, bal[i])
		bal[i] -= pay
		rest -= pay
		if bal[i] <= epsilon {
			bal[i] = 0
			res.payoffMonth[i] = 0
		}
	}

	for month := 1; month <= maxMonths; month++ {
		if total(bal) <= epsilon {
			res.months = month - 1
			res.converged = true
			return res
		}
		before := total(bal)

		var monthInterest float64
		for i := range bal {
			if bal[i] <= 0 {
				continue
			}
			interest := bal[i] * debts[i].APR / 100 / 12
			bal[i] += interest
			monthInterest += interest
			res.totalInterest += interest
		}

		budget := monthlyPayment

		// The focus debt is skipped here — it absorbs the whole remainder below.
		focus := firstUnpaid(bal, order)
		for _, i := range order {
			if bal[i] <= 0 || i == focus || budget <= epsilon {
				continue
			}
			pay := math.Min(math.Min(debts[i].MinPayment, bal[i]), budget)
			bal[i] -= pay
			budget -= pay
			if bal[i] <= epsilon {
				bal[i] = 0
				res.payoffMonth[i] = month
			}
		}

		// Everything left attacks the focus debt, cascading to the next one
		// the moment it clears.
		for budget > epsilon {
			f := firstUnpaid(bal, order)
			if f == -1 {
				break
			}
			pay := math.Min(budget, bal[f])
			bal[f] -= pay
			budget -= pay
			if bal[f] <= epsilon {
				bal[f] = 0
				res.payoffMonth[f] = month
			}
		}

		after := total(bal)
		res.schedule = append(res.schedule, MonthRow{
			Month:     month,
			Date:      start.AddDate(0, month, 0).Format("2006-01"),
			Balance:   round2(after),
			Interest:  round2(monthInterest),
			Principal: round2(math.Max(0, before-after+monthInterest)),
		})

		if after <= epsilon {
			res.months = month
			res.converged = true
			return res
		}
		// The balance didn't move: the payment can't outrun the interest.
		if after >= before-epsilon {
			res.months = month
			res.converged = false
			return res
		}
	}

	res.months = maxMonths
	res.converged = false
	return res
}

// simulateMinimumsOnly is the do-nothing baseline: each debt amortized at its
// own minimum with no rollover, which is what happens if the household changes
// nothing. Debts that never amortize run to the horizon and mark the result
// capped, so "interest saved" is reported as a floor rather than a fiction.
func simulateMinimumsOnly(debts []Debt) runResult {
	res := runResult{payoffMonth: map[int]int{}, converged: true}

	for i, d := range debts {
		bal := d.Balance
		cleared := false
		for month := 1; month <= maxMonths; month++ {
			interest := bal * d.APR / 100 / 12
			bal += interest
			res.totalInterest += interest

			pay := math.Min(d.MinPayment, bal)
			bal -= pay

			if bal <= epsilon {
				res.payoffMonth[i] = month
				res.months = maxInt(res.months, month)
				cleared = true
				break
			}
			// Minimum can't cover the interest — this loan never ends.
			if pay <= interest+epsilon {
				break
			}
		}
		if !cleared {
			res.converged = false
			res.months = maxMonths
		}
	}
	return res
}

// ── ordering ──────────────────────────────────────────────────────────────

func normalizeStrategy(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), Snowball) {
		return Snowball
	}
	return Avalanche
}

// orderFor returns debt indices in repayment priority. Avalanche sorts by rate
// (cheapest total cost); snowball by balance (fastest first win). Ties break on
// the other dimension so the order is deterministic.
func orderFor(debts []Debt, strategy string) []int {
	idx := make([]int, len(debts))
	for i := range debts {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := debts[idx[a]], debts[idx[b]]
		if normalizeStrategy(strategy) == Snowball {
			if x.Balance != y.Balance {
				return x.Balance < y.Balance
			}
			return x.APR > y.APR
		}
		if x.APR != y.APR {
			return x.APR > y.APR
		}
		return x.Balance < y.Balance
	})
	return idx
}

func buildOrder(debts []Debt, order []int, payoff map[int]int, start time.Time) []OrderItem {
	out := make([]OrderItem, 0, len(order))
	for pos, i := range order {
		d := debts[i]
		item := OrderItem{
			Position:    pos + 1,
			Name:        d.Name,
			Balance:     round2(d.Balance),
			APR:         d.APR,
			MinPayment:  round2(d.MinPayment),
			MonthlyCost: round2(d.Balance * d.APR / 100 / 12),
		}
		if m, ok := payoff[i]; ok {
			item.PayoffMonth = m
			item.PayoffDate = start.AddDate(0, m, 0).Format("2006-01")
		} else {
			item.NeverPaysOff = true
		}
		out = append(out, item)
	}
	return out
}

// ── helpers ───────────────────────────────────────────────────────────────

func firstUnpaid(bal []float64, order []int) int {
	for _, i := range order {
		if bal[i] > 0 {
			return i
		}
	}
	return -1
}

func total(bal []float64) float64 {
	var t float64
	for _, b := range bal {
		t += b
	}
	return t
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// currencyFormat describes how to render one currency in the prose this
// engine writes.
//
// This mirrors what the UI produces via Intl.NumberFormat with locale en-US
// and currencyDisplay "narrowSymbol" (see jarvishomeassist-face/lib/currency.ts).
// The two sit side by side — a stat tile beside plan.Summary — so any drift
// between them reads as a bug. Notably: Intl renders SGD as a bare "$", and
// JPY has no minor unit, so yen must never show cents.
type currencyFormat struct {
	symbol string
	digits int
}

var currencyFormats = map[string]currencyFormat{
	"USD": {"$", 2},
	"CAD": {"$", 2},
	"AUD": {"$", 2},
	"SGD": {"$", 2},
	"MYR": {"RM", 2},
	"EUR": {"€", 2},
	"GBP": {"£", 2},
	"JPY": {"¥", 0},
	"INR": {"₹", 2},
}

// formatFor resolves a currency code, falling back to USD for anything
// unrecognised so an unknown code can never produce a bare number.
func formatFor(code string) currencyFormat {
	if f, ok := currencyFormats[strings.ToUpper(strings.TrimSpace(code))]; ok {
		return f
	}
	return currencyFormats["USD"]
}

// money renders an amount for the human-readable warnings and summary:
// grouped thousands, the currency's own minor-unit precision, and a trailing
// all-zero fraction dropped so headline figures read cleanly.
func money(v float64, cf currencyFormat) string {
	// Round half away from zero to match Intl's default "halfExpand" mode.
	// strconv.FormatFloat rounds half to even, which would render 1234.5 JPY
	// as ¥1,234 while the UI beside it shows ¥1,235.
	pow := math.Pow(10, float64(cf.digits))
	abs := math.Round(math.Abs(v)*pow) / pow

	s := strconv.FormatFloat(abs, 'f', cf.digits, 64)
	if cf.digits > 0 {
		s = strings.TrimSuffix(s, "."+strings.Repeat("0", cf.digits))
	}

	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}

	// Intl separates multi-character symbols from the digits ("RM 6,250") but
	// not single-character ones ("$6,250") — and the separator it uses is a
	// NO-BREAK SPACE (U+00A0), not a plain one. Match it exactly.
	sep := ""
	if len([]rune(cf.symbol)) > 1 {
		sep = nbsp
	}
	if v < 0 {
		return "-" + cf.symbol + sep + out
	}
	return cf.symbol + sep + out
}

func summarize(p Plan, cf currencyFormat) string {
	var b strings.Builder
	b.WriteString("You have " + money(p.TotalDebt, cf) + " of debt and " + money(p.Survival.MonthlyIncome, cf) + " monthly income. ")
	b.WriteString("After fixed expenses, you can safely allocate " + money(p.MonthlyPaymentTarget, cf) + "/month. ")
	if p.Feasible && p.MonthsToFreedom > 0 {
		b.WriteString("Estimated debt-free date: " + p.DebtFreeDate + " (" + strconv.Itoa(p.MonthsToFreedom) + " months)")
		if p.InterestSaved > 0 {
			b.WriteString(", saving " + money(p.InterestSaved, cf) + " in interest")
		}
		b.WriteString(".")
	} else {
		b.WriteString("That isn't enough to clear these balances — the plan needs a higher payment or lower rates.")
	}
	return b.String()
}
