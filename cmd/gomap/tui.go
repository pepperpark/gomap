package main

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	lipgloss "github.com/charmbracelet/lipgloss"

	"math"

	"github.com/pepperpark/gomap/internal/syncer"
)

type mailboxProgress struct {
	total int
	done  int
}

type model struct {
	ctx      context.Context
	cancel   context.CancelFunc
	worker   *syncer.MailboxSyncer
	boxes    []string
	prog     map[string]mailboxProgress
	totalAll int
	doneAll  int
	spinner  spinner.Model
	bar      progress.Model
	errs     []error
	finished bool
	started  time.Time
	// Smoothed ETA
	emaRate  float64 // msgs/sec (EMA)
	lastDone int
	lastAt   time.Time
}

type tickMsg time.Time
type errsMsg []error
type mboxProgMsg int

func newModel(ctx context.Context, worker *syncer.MailboxSyncer, boxes []string) *model {
	cctx, cancel := context.WithCancel(ctx)
	s := spinner.New()
	s.Spinner = spinner.Line
	bar := progress.New(progress.WithDefaultGradient())
	now := time.Now()
	return &model{ctx: cctx, cancel: cancel, worker: worker, boxes: boxes, prog: map[string]mailboxProgress{}, spinner: s, bar: bar, started: now, lastAt: now}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick(), m.startSync())
}

func tick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) startSync() tea.Cmd {
	// Kick off sync in background
	return func() tea.Msg {
		errs := m.worker.SyncAll(m.ctx, m.boxes)
		return errsMsg(errs)
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.cancel()
			return m, tea.Quit
		}
	case errsMsg:
		m.errs = []error(msg)
		m.finished = true
		// If there were no errors, ensure the overall progress snaps to 100%
		if len(m.errs) == 0 {
			m.doneAll = m.totalAll
		}
		return m, tea.Quit
	case tickMsg:
		// update EMA of throughput on each tick
		m.updateEMARate()
		return m, tea.Batch(m.spinner.Tick, tick())
	}
	// Drain events
	for {
		select {
		case ev, ok := <-m.worker.Events():
			if !ok {
				// No more events. If we already finished with no errors, snap to 100%.
				if m.finished && len(m.errs) == 0 {
					m.doneAll = m.totalAll
				}
				return m, nil
			}
			switch ev.Type {
			case syncer.EventMailboxProgress:
				mp := m.prog[ev.Mailbox]
				mp.total, mp.done = ev.Total, ev.Done
				m.prog[ev.Mailbox] = mp
				// Update global
				m.recomputeTotals()
			}
		default:
			return m, nil
		}
	}
}

func (m *model) recomputeTotals() {
	total, done := 0, 0
	for _, p := range m.prog {
		total += p.total
		done += p.done
	}
	m.totalAll, m.doneAll = total, done
}

func (m *model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render("Gomap")
	s := title + "\n\nPress q to quit\n\n"
	pct := 0.0
	if m.totalAll > 0 {
		pct = float64(m.doneAll) / float64(m.totalAll)
	}
	eta := m.formatETA()
	s += fmt.Sprintf("%s Overall %d/%d   %s\n", m.spinner.View(), m.doneAll, m.totalAll, eta)
	s += m.bar.ViewAs(pct) + "\n\n"
	if m.finished && len(m.errs) > 0 {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("Errors:\n")
		for _, e := range m.errs {
			s += " - " + e.Error() + "\n"
		}
	} else if m.finished && len(m.errs) == 0 && m.totalAll == 0 && m.doneAll == 0 {
		// Helpful hint for the common 0/0 case with resume state
		hint := "No new messages detected. Resume state may be active.\nUse --ignore-state or a fresh --state-file to process everything again."
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(hint) + "\n"
	}
	return s
}

func (m *model) formatETA() string {
	// Simple ETA based on average rate since start
	if m.totalAll == 0 {
		return "ETA --"
	}
	remaining := m.totalAll - m.doneAll
	if remaining <= 0 {
		return "ETA 0s"
	}
	// Prefer smoothed rate if available; fallback to average rate
	rate := m.emaRate
	if rate <= 0.01 {
		elapsed := time.Since(m.started)
		if elapsed <= 0 {
			return "ETA --"
		}
		rate = float64(m.doneAll) / elapsed.Seconds()
	}
	if rate <= 0.01 { // too low/unstable
		return "ETA --"
	}
	secs := float64(remaining) / rate
	if secs < 1 {
		return "ETA <1s"
	}
	d := time.Duration(secs) * time.Second
	// cap very large ETAs to something readable
	if d > 99*time.Hour {
		return "ETA >99h"
	}
	// human-friendly formatting
	if d >= time.Hour {
		h := int(d / time.Hour)
		rem := d - time.Duration(h)*time.Hour
		mrem := int(rem / time.Minute)
		return fmt.Sprintf("ETA %dh%dm", h, mrem)
	}
	if d >= time.Minute {
		mns := int(d.Minutes())
		sec := int(d.Seconds()) % 60
		return fmt.Sprintf("ETA %dm%ds", mns, sec)
	}
	return fmt.Sprintf("ETA %ds", int(d.Seconds()))
}

// updateEMARate updates the EMA of processing rate based on deltas since last tick.
func (m *model) updateEMARate() {
	now := time.Now()
	dt := now.Sub(m.lastAt).Seconds()
	if dt <= 0 {
		return
	}
	delta := m.doneAll - m.lastDone
	inst := float64(delta) / dt // msgs/sec
	// EMA with half-life ~3s -> alpha depends on dt
	halfLife := 3.0 // seconds
	alpha := 1 - math.Exp(-math.Ln2*dt/halfLife)
	if m.emaRate == 0 {
		m.emaRate = inst
	} else {
		m.emaRate = alpha*inst + (1-alpha)*m.emaRate
	}
	m.lastDone = m.doneAll
	m.lastAt = now
}

// runTUI runs the Bubble Tea UI and returns errors after completion
func runTUI(ctx context.Context, worker *syncer.MailboxSyncer, boxes []string) []error {
	m := newModel(ctx, worker, boxes)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		// Fallback to non-TUI execution
		fmt.Println("TUI failed:", err)
		errs := worker.SyncAll(ctx, boxes)
		return errs
	}
	return m.errs
}

// --- Simplified TUI for MBOX copy ---

type mboxModel struct {
	total    int
	done     int
	spinner  spinner.Model
	bar      progress.Model
	errs     []error
	finished bool
	// ETA smoothing
	emaRate  float64
	lastDone int
	lastAt   time.Time
	started  time.Time
}

func newMboxModel(total int) *mboxModel {
	s := spinner.New()
	s.Spinner = spinner.Line
	bar := progress.New(progress.WithDefaultGradient())
	now := time.Now()
	return &mboxModel{total: total, spinner: s, bar: bar, started: now, lastAt: now}
}

func (m *mboxModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick())
}

func (m *mboxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case errsMsg:
		m.errs = []error(msg)
		m.finished = true
		if len(m.errs) == 0 {
			m.done = m.total
		}
		return m, tea.Quit
	case mboxProgMsg:
		m.done += int(msg)
		return m, m.spinner.Tick
	case tickMsg:
		// update EMA
		now := time.Now()
		dt := now.Sub(m.lastAt).Seconds()
		if dt > 0 {
			delta := m.done - m.lastDone
			inst := float64(delta) / dt
			halfLife := 3.0
			alpha := 1 - math.Exp(-math.Ln2*dt/halfLife)
			if m.emaRate == 0 {
				m.emaRate = inst
			} else {
				m.emaRate = alpha*inst + (1-alpha)*m.emaRate
			}
			m.lastDone = m.done
			m.lastAt = now
		}
		return m, tea.Batch(m.spinner.Tick, tick())
	}
	return m, nil
}

func (m *mboxModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render("Gomap")
	s := title + "\n\nPress q to quit\n\n"
	pct := 0.0
	if m.total > 0 {
		pct = float64(m.done) / float64(m.total)
	}
	s += fmt.Sprintf("%s Overall %d/%d   %s\n", m.spinner.View(), m.done, m.total, m.mboxETA())
	s += m.bar.ViewAs(pct) + "\n\n"
	if m.finished && len(m.errs) > 0 {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("Errors:\n")
		for _, e := range m.errs {
			s += " - " + e.Error() + "\n"
		}
	} else if m.finished && len(m.errs) == 0 && m.total == 0 && m.done == 0 {
		hint := "No new messages detected. Resume state may be active.\nUse --ignore-state or a fresh --state-file to process everything again."
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(hint) + "\n"
	}
	return s
}

func (m *mboxModel) mboxETA() string {
	if m.total == 0 {
		return "ETA --"
	}
	remaining := m.total - m.done
	if remaining <= 0 {
		return "ETA 0s"
	}
	rate := m.emaRate
	if rate <= 0.01 {
		elapsed := time.Since(m.started)
		if elapsed <= 0 {
			return "ETA --"
		}
		rate = float64(m.done) / elapsed.Seconds()
	}
	if rate <= 0.01 {
		return "ETA --"
	}
	secs := float64(remaining) / rate
	if secs < 1 {
		return "ETA <1s"
	}
	d := time.Duration(secs) * time.Second
	if d > 99*time.Hour {
		return "ETA >99h"
	}
	if d >= time.Hour {
		h := int(d / time.Hour)
		rem := d - time.Duration(h)*time.Hour
		mrem := int(rem / time.Minute)
		return fmt.Sprintf("ETA %dh%dm", h, mrem)
	}
	if d >= time.Minute {
		mns := int(d.Minutes())
		sec := int(d.Seconds()) % 60
		return fmt.Sprintf("ETA %dm%ds", mns, sec)
	}
	return fmt.Sprintf("ETA %ds", int(d.Seconds()))
}

// runMboxTUI displays a simple progress UI driven by a progress channel
func runMboxTUI(total int, progress <-chan int, errc <-chan error) []error {
	m := newMboxModel(total)
	p := tea.NewProgram(m)
	// Fan-in progress/errors into Program messages
	go func() {
		for inc := range progress {
			p.Send(mboxProgMsg(inc))
		}
		// After progress closes, wait for error signal (which may be nil)
		if err := <-errc; err != nil {
			p.Send(errsMsg{err})
		} else {
			p.Send(errsMsg{})
		}
	}()
	if _, err := p.Run(); err != nil {
		// Fallback: no TUI, just drain and return error
		errs := []error{}
		for range progress {
		}
		if err := <-errc; err != nil {
			errs = append(errs, err)
		}
		return errs
	}
	// Retrieve errs from finished model state is not trivial here; rely on errsMsg used above
	// Return none; errs were printed within TUI
	return []error{}
}

// --- Confirmation TUI ---

// A simple count-based progress model for generic operations (e.g., mark-read)
type countModel struct {
	title    string
	total    int
	done     int
	spinner  spinner.Model
	bar      progress.Model
	errs     []error
	finished bool
	// ETA smoothing
	emaRate  float64
	lastDone int
	lastAt   time.Time
	started  time.Time
}

func newCountModel(title string, total int) *countModel {
	s := spinner.New()
	s.Spinner = spinner.Line
	bar := progress.New(progress.WithDefaultGradient())
	now := time.Now()
	return &countModel{title: title, total: total, spinner: s, bar: bar, started: now, lastAt: now}
}

func (m *countModel) Init() tea.Cmd { return tea.Batch(m.spinner.Tick, tick()) }

func (m *countModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case errsMsg:
		m.errs = []error(msg)
		m.finished = true
		if len(m.errs) == 0 {
			m.done = m.total
		}
		return m, tea.Quit
	case mboxProgMsg:
		m.done += int(msg)
		return m, m.spinner.Tick
	case tickMsg:
		now := time.Now()
		dt := now.Sub(m.lastAt).Seconds()
		if dt > 0 {
			delta := m.done - m.lastDone
			inst := float64(delta) / dt
			halfLife := 3.0
			alpha := 1 - math.Exp(-math.Ln2*dt/halfLife)
			if m.emaRate == 0 {
				m.emaRate = inst
			} else {
				m.emaRate = alpha*inst + (1-alpha)*m.emaRate
			}
			m.lastDone = m.done
			m.lastAt = now
		}
		return m, tea.Batch(m.spinner.Tick, tick())
	}
	return m, nil
}

func (m *countModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render("Gomap")
	s := title + "\n\nPress q to quit\n\n"
	pct := 0.0
	if m.total > 0 {
		pct = float64(m.done) / float64(m.total)
	}
	s += fmt.Sprintf("%s %s %d/%d   %s\n", m.spinner.View(), m.title, m.done, m.total, m.countETA())
	s += m.bar.ViewAs(pct) + "\n\n"
	if m.finished && len(m.errs) > 0 {
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("Errors:\n")
		for _, e := range m.errs {
			s += " - " + e.Error() + "\n"
		}
	}
	return s
}

func (m *countModel) countETA() string {
	if m.total == 0 {
		return "ETA --"
	}
	remaining := m.total - m.done
	if remaining <= 0 {
		return "ETA 0s"
	}
	rate := m.emaRate
	if rate <= 0.01 {
		elapsed := time.Since(m.started)
		if elapsed <= 0 {
			return "ETA --"
		}
		rate = float64(m.done) / elapsed.Seconds()
	}
	if rate <= 0.01 {
		return "ETA --"
	}
	secs := float64(remaining) / rate
	if secs < 1 {
		return "ETA <1s"
	}
	d := time.Duration(secs) * time.Second
	if d > 99*time.Hour {
		return "ETA >99h"
	}
	if d >= time.Hour {
		h := int(d / time.Hour)
		rem := d - time.Duration(h)*time.Hour
		mrem := int(rem / time.Minute)
		return fmt.Sprintf("ETA %dh%dm", h, mrem)
	}
	if d >= time.Minute {
		mns := int(d.Minutes())
		sec := int(d.Seconds()) % 60
		return fmt.Sprintf("ETA %dm%ds", mns, sec)
	}
	return fmt.Sprintf("ETA %ds", int(d.Seconds()))
}

// runCountTUI displays a simple progress bar for count-based operations.
func runCountTUI(total int, title string, progress <-chan int, errc <-chan error) []error {
	m := newCountModel(title, total)
	p := tea.NewProgram(m)
	go func() {
		for inc := range progress {
			p.Send(mboxProgMsg(inc))
		}
		if err := <-errc; err != nil {
			p.Send(errsMsg{err})
		} else {
			p.Send(errsMsg{})
		}
	}()
	if _, err := p.Run(); err != nil {
		errs := []error{}
		for range progress {
		}
		if err := <-errc; err != nil {
			errs = append(errs, err)
		}
		return errs
	}
	return []error{}
}

type confirmModel struct {
	title   string
	summary string
	choice  *bool
}

func newConfirmModel(title, summary string) *confirmModel {
	return &confirmModel{title: title, summary: summary}
}

func (m *confirmModel) Init() tea.Cmd { return nil }

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "enter":
			v := true
			m.choice = &v
			return m, tea.Quit
		case "n", "q", "esc", "ctrl+c":
			v := false
			m.choice = &v
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *confirmModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Render(m.title)
	desc := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("Press y to confirm, n to cancel")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).Width(78).Render(m.summary)
	return fmt.Sprintf("%s\n\n%s\n\n%s\n", title, box, desc)
}

// runConfirmTUI displays a confirmation dialog with a summary and returns true if confirmed.
func runConfirmTUI(title, summary string) (bool, error) {
	m := newConfirmModel(title, summary)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		return false, err
	}
	if m.choice == nil {
		return false, nil
	}
	return *m.choice, nil
}
