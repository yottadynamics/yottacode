package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// popupOpen reports whether any decision modal or picker popup is
// currently intercepting input. anyOverlayOpen (model.go) already covers
// every picker/panel flag activePopupBody's branches key off of; the
// three additional flags here (awaitingApproval/awaitingPathTrust/
// loopExitConfirmOpen) are deliberately kept OUT of anyOverlayOpen
// itself (it has its own callers/tests with a narrower meaning) rather
// than widening that function for this. All three now have real
// per-surface click handling (handleApprovalModalClick,
// handlePathTrustModalClick, handleLoopExitConfirmClick) — clicking
// blank chrome on any of them still no-ops, same as it does on the
// keyboard side for keys a picker doesn't bind.
func (m Model) popupOpen() bool {
	return m.anyOverlayOpen() || m.awaitingApproval || m.awaitingPathTrust || m.loopExitConfirmOpen
}

// interactiveMouseOpen reports whether yottacode currently owns an explicit
// interactive surface that benefits from mouse reporting. Normal conversation
// mouse stays terminal-native so right-click menus, paste, and selection work.
func (m Model) interactiveMouseOpen() bool {
	return m.popupOpen() || m.paletteOpen || m.filePaletteOpen
}

// dismissStaticPopup closes whichever any-key-dismiss static panel is
// open — cheatsheet, bare-/loop status, usage, experimental, help,
// context-report — mirroring the identical dismiss logic the KeyPressMsg
// case already has for each (model.go). No cursor/item concept in any
// of these, so a click anywhere just dismisses, same as any keypress.
func (m Model) dismissStaticPopup() Model {
	m.cheatsheetOpen = false
	m.loopListOpen = false
	m.usageOpen = false
	m.usagePanel = ""
	m.usageScrollOffset = 0
	m.inspectOpen = false
	m.inspectPanel = ""
	m.inspectScrollOffset = 0
	m.experimentalOpen = false
	m.experimentalPanel = ""
	m.helpOpen = false
	m.helpPanel = ""
	m.contextReportOpen = false
	m.contextReportBody = ""
	return m
}

// popupCloseHit reports whether a screen coordinate landed on the popup's
// top-right close affordance. It intentionally accepts the glyph cell and its
// one-cell breathing room so the target is usable with terminal mouse reporting.
func popupCloseHit(box string, originX, originY, screenX, screenY int) bool {
	if screenY != originY {
		return false
	}
	bw := lipgloss.Width(box)
	if bw < 6 {
		return false
	}
	return screenX >= originX+bw-3 && screenX <= originX+bw-2
}

// handleScrollableStaticPopupClick routes mouse clicks inside scrollable static
// panels. The popup stays open; clicks on the hint row act like its ↑/↓ labels.
func (m Model) handleScrollableStaticPopupClick(box string, msg tea.MouseClickMsg) (Model, bool) {
	if m.usageOpen && m.usageMaxScrollOffset() == 0 {
		return m, false
	}
	if m.inspectOpen && m.inspectMaxScrollOffset() == 0 {
		return m, false
	}
	if !m.usageOpen && !m.inspectOpen {
		return m, false
	}
	originX, originY := m.popupOrigin(box)
	boxWidth := lipgloss.Width(box)
	boxHeight := lipgloss.Height(box)
	// The scroll hint lives on the final content row, just above the bottom
	// border. Treat its left half as the ↑ affordance and its right half as ↓.
	if msg.Y != originY+boxHeight-2 || msg.X < originX || msg.X >= originX+boxWidth {
		return m, true
	}
	if msg.X-originX < boxWidth/2 {
		return m.scrollPopupLines(-1), true
	}
	return m.scrollPopupLines(1), true
}

const popupMouseWheelLines = 3

// scrollPopupLines applies keyboard-equivalent scrolling to whichever fixed
// static popup is currently open, clamping at the popup content bounds.
func (m Model) scrollPopupLines(delta int) Model {
	if m.usageOpen {
		m.usageScrollOffset = min(max(m.usageScrollOffset+delta, 0), m.usageMaxScrollOffset())
	}
	if m.inspectOpen {
		m.inspectScrollOffset = min(max(m.inspectScrollOffset+delta, 0), m.inspectMaxScrollOffset())
	}
	return m
}

// handlePopupCloseClick centralizes the shared × affordance. It mirrors each
// surface's Esc behavior instead of inventing a second close path.
func (m Model) handlePopupCloseClick(box string, msg tea.MouseClickMsg) (Model, tea.Cmd, bool) {
	ox, oy := m.popupOrigin(box)
	if !popupCloseHit(box, ox, oy, msg.X, msg.Y) {
		return m, nil, false
	}
	out, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	return out.(Model), cmd, true
}

// before its first body row/column. Both popupBox (rounded, Padding(0,1))
// and renderLabeledBox (sharp corners, no explicit padding call) have
// zero VERTICAL padding — confirmed by reading both — so body row N
// always sits at box-row N+1 (the top border) regardless of which family
// framed it; column offset is +2 for "│ " (border + one padding cell)
// either way. ok=false when the click landed outside the box's interior.
func bodyPoint(box string, originX, originY, screenX, screenY int) (row, col int, ok bool) {
	row = screenY - originY - 1
	col = screenX - originX - 2
	bh, bw := lipgloss.Height(box), lipgloss.Width(box)
	if row < 0 || row >= bh-2 || col < 0 || col >= bw-2 {
		return 0, 0, false
	}
	return row, col, true
}

// handleModelPickerClick resolves a click inside the open model picker
// to either a tab switch or an item selection. Re-renders the picker
// with a fresh hit accumulator (see pickerHits' doc for why recompute-
// don't-cache) to find what's under the click, then reuses the
// picker's own existing keyboard path to act on it — the click handler
// never needs to know commitModelChoice exists.
func (m Model) handleModelPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.modelPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderModelPicker(m.modelPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok {
		return m, nil // clicked chrome/whitespace — no keyboard equivalent either
	}
	switch kind {
	case hitTab:
		return m, m.cyclePickerProviderTo(index)
	case hitItem:
		m.modelPicker.cursor = index
		m.modelPicker.clampCursorToWindow()
		return m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	return m, nil
}

// handleCheckpointsPickerClick resolves a click on either of the
// picker's two screens (the checkpoint list, or the action menu once
// one is picked) to a cursor move + synthesized Enter — the same
// screen-branch logic updateCheckpointsPicker's own Enter case already
// has, so the click handler doesn't need to duplicate it.
func (m Model) handleCheckpointsPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.checkpointsPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderCheckpointsPicker(m.checkpointsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	if m.checkpointsPicker.picked != nil {
		m.checkpointsPicker.actionIdx = index
	} else {
		m.checkpointsPicker.cursor = index
	}
	return m.updateCheckpointsPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSkillsPickerClick resolves a click on the skills Catalog picker:
// a tab click switches Official/Bundled (mirroring the Tab-key handler's
// cursor/status reset — there's no cyclePickerProvider-style helper to
// reuse here since the tab is a direct index, not a relative step), an
// item click replays Enter (install/view) through updateSkillsPicker,
// which already gates on busy/filterMode itself.
func (m Model) handleSkillsPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.skillsPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderSkillsPicker(m.skillsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok {
		return m, nil
	}
	switch kind {
	case hitTab:
		if m.skillsPicker.busy != "" || m.skillsPicker.filterMode {
			return m, nil
		}
		m.skillsPicker.tab = skillsCatalogTab(index)
		m.skillsPicker.cursor = 0
		m.skillsPicker.status = ""
		return m, nil
	case hitItem:
		m.skillsPicker.cursor = index
		return m.updateSkillsPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	return m, nil
}

// handleMemoryPickerClick resolves a click on either of the picker's two
// modes (root menu, or the browse-mode entry list once one of the
// "Browse … memories" rows is picked) to a cursor move + synthesized
// Enter — mirrors updateMemoryPicker's own mode dispatch, so a browse-
// mode click routes through updateMemoryBrowse the same way a keypress
// does. Browse mode's single-letter 'd'/'f' hotkeys (delete / open
// folder) stay keyboard-only — not part of "click an item to select
// it."
func (m Model) handleMemoryPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.memoryPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderMemoryPicker(m.memoryPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	if m.memoryPicker.mode == memoryBrowseMode {
		m.memoryPicker.entryCursor = index
		return m.updateMemoryBrowse(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	m.memoryPicker.cursor = index
	return m.updateMemoryPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleCodeMapPickerClick resolves a click on a code-map row to a
// cursor move + synthesized Enter (expand/select, same as
// updateCodeMapPicker's own Enter case). No-ops while the map is
// loading, errored, or showing a diagram (no rows registered by the
// renderer in those states, so hits.resolve naturally returns ok=false).
func (m Model) handleCodeMapPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.codeMapPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderCodeMapPicker(m.codeMapPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.codeMapPicker.cursor = index
	return m.updateCodeMapPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleEffortPickerClick resolves a click on an effort-level row to a
// cursor move + synthesized Enter (same as updateEffortPicker's own
// Enter case). Note renderEffortPicker takes no width parameter, unlike
// most other pickers.
func (m Model) handleEffortPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.effortPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderEffortPicker(m.effortPicker, hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.effortPicker.cursor = index
	return m.updateEffortPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleMCPPickerClick resolves a click on the MCP picker's menu or one
// of its three list-shaped screens (List/Remove/Logs, all rendered by
// the shared renderMCPServerList) to a cursor move + synthesized Enter.
// The Add form has no click targets in this phase — its text fields stay
// keyboard-only (tab/shift+tab to focus, same as every other text input
// in this codebase).
func (m Model) handleMCPPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.mcpPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderMCPPicker(m.mcpPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	switch m.mcpPicker.mode {
	case mcpMenuMode:
		m.mcpPicker.menuCursor = index
	default:
		m.mcpPicker.listCursor = index
	}
	return m.updateMCPPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleRecallPickerClick resolves a click on a recall result row to a
// cursor move + synthesized Enter (previews the session, same as
// updateRecallPicker's own Enter case). No-op while in preview mode —
// the preview panel has no row concept, so renderRecallPicker registers
// no hits there and hits.resolve naturally returns ok=false.
func (m Model) handleRecallPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.recallPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderRecallPicker(m.recallPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.recallPicker.cursor = index
	return m.updateRecallPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleRouterPickerClick resolves a click on either of the router
// picker's two screens (the menu, or the model sub-list once a row is
// being filled) to a cursor move + synthesized Enter — mirrors
// updateRouterPicker's own selecting-vs-menu dispatch.
func (m Model) handleRouterPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.routerPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderRouterPicker(m.routerPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	if m.routerPicker.selecting != "" {
		m.routerPicker.modelCursor = index
		m.routerPicker.clampWindow()
	} else {
		m.routerPicker.cursor = index
	}
	return m.updateRouterPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleProviderPickerClick resolves a click on one of the provider
// picker's three row-list screens (top menu, Use/Remove list, Add-kind
// catalog) to a cursor move + synthesized Enter — mirrors
// updateProviderPicker's own per-mode Enter dispatch. The Add-fields
// form (text inputs, plus its embedded family/curated-model Up/Down
// sub-widgets) has no click targets in this phase, same treatment as
// the MCP picker's Add form.
func (m Model) handleProviderPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.providerPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderProviderPicker(m.providerPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	switch m.providerPicker.mode {
	case providerMenuMode:
		m.providerPicker.menuCursor = index
	case providerUsePickerMode, providerRemovePickerMode:
		m.providerPicker.usePickerCursor = index
	case providerAddKindMode:
		m.providerPicker.addKindCursor = index
	default:
		return m, nil
	}
	return m.updateProviderPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSandboxPickerClick resolves a click on a sandbox-mode row to a
// cursor move + synthesized Enter — same two-step preview/confirm flow
// as the keyboard: the first click on a row previews it, a second click
// on the (now-current) cursor row confirms and persists, matching
// updateSandboxPicker's own p.confirming gate.
func (m Model) handleSandboxPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.sandboxPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderSandboxPicker(m.sandboxPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	if sandboxMode(index) != m.sandboxPicker.cursor {
		m.sandboxPicker.confirming = false
	}
	m.sandboxPicker.cursor = sandboxMode(index)
	return m.updateSandboxPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handlePlansPickerClick resolves a click on a saved-plan row to a
// cursor move + synthesized Enter (resumes the plan, same as
// updatePlansPicker's own Enter case).
func (m Model) handlePlansPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.plansPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderPlansPicker(m.plansPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.plansPicker.cursor = index
	return m.updatePlansPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSkillsMenuClick resolves a click on either of the skills menu's
// row-list screens (the top-level menu, or the uninstall pick list once
// entered) to a cursor move + synthesized Enter. The Install screen's
// textinput has no click target in this phase.
func (m Model) handleSkillsMenuClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.skillsMenu == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderSkillsMenu(m.skillsMenu, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	if m.skillsMenu.mode == skillsMenuUninstallPick {
		m.skillsMenu.uninstallCursor = index
	} else {
		m.skillsMenu.cursor = index
	}
	return m.updateSkillsMenu(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSessionsPickerClick resolves a click on either the sessions
// picker's top menu or one of its three list sub-screens (Load/Rename/
// Export, all rendered by renderSessionsList) to a cursor move +
// synthesized Enter — mirrors updateSessionsPicker's own mode dispatch.
// The Resume/Rename/Export textinput screens have no click targets in
// this phase.
func (m Model) handleSessionsPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.sessionsPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderSessionsPicker(m.sessionsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	switch m.sessionsPicker.mode {
	case sessionsMenuMode:
		m.sessionsPicker.menuCursor = index
	case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
		m.sessionsPicker.listCursor = index
	default:
		return m, nil
	}
	return m.updateSessionsPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSubagentsPickerClick resolves a click on a subagents-picker row
// (tasks or types view, same list shown by rowCount) to a cursor move +
// synthesized Enter — a no-op in types mode, same as the keyboard,
// since updateSubagentsPicker's Enter case only opens a transcript in
// tasks mode.
func (m Model) handleSubagentsPickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.subagentsPicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderSubagentsPicker(m.subagentsPicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.subagentsPicker.cursor = index
	m.subagentsPicker.status = ""
	return m.updateSubagentsPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleThemePickerClick resolves a click on a theme-list row to a
// cursor move (which live-applies the preview, same as arrowing) +
// synthesized Enter (commits + persists). The list uses column-bounded
// hitTab regions (not hitItem) so a click landing in the preview pane
// beside the list — which shares the same rows — doesn't also resolve
// to a list item; both kinds are treated identically here since this
// picker has no real tab strip of its own.
func (m Model) handleThemePickerClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.themePicker == nil {
		return m, nil
	}
	hits := &pickerHits{}
	box := popupBox(renderThemePicker(m.themePicker, m.popupWidth(), hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || (kind != hitItem && kind != hitTab) {
		return m, nil
	}
	m.themePicker.cursor = index
	m = applyHighlightedTheme(m)
	return m.updateThemePicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handlePermissionsOverlayClick resolves a click on the shared/local row
// to a cursor move + synthesized Enter (opens that file in vim, same as
// updatePermissionsPicker's own Enter case).
func (m Model) handlePermissionsOverlayClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	hits := &pickerHits{}
	box := popupBox(renderPermissionsOverlay(m, hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.permissionsCursor = index
	return m.updatePermissionsPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleLoopExitConfirmClick resolves a click on the "Exit anyway"/"Stay"
// rows to a cursor move + synthesized Enter — same commit path as the
// keyboard. updateLoopExitConfirm returns tea.Model (it's dispatched
// straight from Update's top-level switch, not through the Model-typed
// picker helpers), so the result is type-asserted back to Model here.
func (m Model) handleLoopExitConfirmClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	hits := &pickerHits{}
	box := popupBox(renderLoopExitConfirm(m, hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.loopExitConfirmCursor = index
	out, cmd := m.updateLoopExitConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	return out.(Model), cmd
}

// handleApprovalModalClick resolves a click on the approval modal's (or,
// for the plan-mode boundary tools, the plan-approval card's) hotkey
// grid to a synthesized keypress replayed straight through the top-
// level Update — reusing the ~200-line inline awaitingApproval key-
// switch verbatim rather than duplicating its per-tool branching here.
// These renderers self-border via renderLabeledBox (unlike the picker
// popups) and are composited as-is (see activePopupBody in model.go),
// so — unlike every other click handler in this file — the render
// output is NOT wrapped in popupBox before computing origin/bodyPoint.
func (m Model) handleApprovalModalClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	hits := &pickerHits{}
	var box string
	switch m.approvalTool {
	case "exit_plan_mode":
		box = renderPlanApprovalCard(m.width, hits)
	case "enter_plan_mode":
		box = renderEnterPlanApprovalCard(m.width, hits)
	default:
		box = renderApprovalModal(m, hits)
	}
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	_, _, key, ok := hits.resolve(row, col)
	if !ok || key == "" {
		return m, nil
	}
	out, cmd := m.Update(tea.KeyPressMsg{Text: key})
	return out.(Model), cmd
}

// handlePathTrustModalClick resolves a click on the path-trust modal's
// [1]/[2]/[3] rows the same way handleApprovalModalClick does — a
// synthesized keypress through the top-level Update, reusing the
// existing inline awaitingPathTrust key-switch.
func (m Model) handlePathTrustModalClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	hits := &pickerHits{}
	box := renderPathTrustModal(m, hits)
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	_, _, key, ok := hits.resolve(row, col)
	if !ok || key == "" {
		return m, nil
	}
	out, cmd := m.Update(tea.KeyPressMsg{Text: key})
	return out.(Model), cmd
}

// handleEmbedSetupClick resolves a click on an embedding-model row to a
// cursor move + synthesized Enter (starts the pull, same as
// updateEmbedSetup's own Enter case). No-op while a pull is already in
// progress — renderEmbedSetup registers no rows in that state, so
// hits.resolve naturally returns ok=false.
func (m Model) handleEmbedSetupClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	hits := &pickerHits{}
	box := popupBox(m.renderEmbedSetup(hits))
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	kind, index, _, ok := hits.resolve(row, col)
	if !ok || kind != hitItem {
		return m, nil
	}
	m.embedSetupCursor = index
	return m.updateEmbedSetup(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// handleSlashPaletteClick turns a main slash-palette row click into the same
// highlighted-row Enter path as the keyboard, so args-required commands fill
// their prefix and no-args commands run immediately.
func (m Model) handleSlashPaletteClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if !m.paletteOpen || len(m.paletteFiltered) == 0 {
		return m, nil
	}
	row, ok := m.inlinePaletteRow(msg.Y, len(m.paletteFiltered), slashPaletteVisible, m.paletteOffset)
	if !ok {
		return m, nil
	}
	m.paletteIndex = m.paletteOffset + row
	chosen := m.paletteFiltered[m.paletteIndex]
	if chosen.Args != "" {
		m.textInput.SetValue("/" + chosen.Name + " ")
		m.textInput.CursorEnd()
		m.paletteOpen = false
		m.paletteIndex = 0
		m.paletteOffset = 0
		return m, nil
	}
	m.textInput.SetValue("")
	m.paletteOpen = false
	m.paletteIndex = 0
	m.paletteOffset = 0
	return m.runSlash("/" + chosen.Name)
}

// handleFilePaletteClick turns an @file palette row click into the same attach
// path as Enter/Tab, preserving directory expansion behavior.
func (m Model) handleFilePaletteClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if !m.filePaletteOpen || len(m.filePaletteFiltered) == 0 {
		return m, nil
	}
	row, ok := m.inlinePaletteRow(msg.Y, len(m.filePaletteFiltered), filePaletteVisible, m.filePaletteOffset)
	if !ok {
		return m, nil
	}
	m.filePaletteIndex = m.filePaletteOffset + row
	out, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return out.(Model), cmd
}

// handleMouseClick resolves a mouse-down event to whichever surface is
// currently showing it: a decision modal (dispatched to its own click
// handler), a picker popup (dispatched to that picker's own click
// handler — every picker/panel has one now), or — with nothing open —
// the transcript (begins a text selection) vs. the footer (Phase E adds
// cmdline click-to-cursor there; until then, a footer click just clears
// any stray selection).
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if box, ok := m.activePopupBody(); ok {
		if out, cmd, closed := m.handlePopupCloseClick(box, msg); closed {
			return out, cmd
		}
		if out, ok := m.handleScrollableStaticPopupClick(box, msg); ok {
			return out, nil
		}
	}
	if m.awaitingPathTrust {
		return m.handlePathTrustModalClick(msg)
	}
	if m.awaitingApproval {
		return m.handleApprovalModalClick(msg)
	}
	if m.paletteOpen {
		return m.handleSlashPaletteClick(msg)
	}
	if m.filePaletteOpen {
		return m.handleFilePaletteClick(msg)
	}
	if m.loopExitConfirmOpen {
		return m.handleLoopExitConfirmClick(msg)
	}
	if m.cheatsheetOpen || m.loopListOpen || m.usageOpen || m.inspectOpen || m.experimentalOpen || m.helpOpen || m.contextReportOpen {
		return m.dismissStaticPopup(), nil
	}
	if m.modelPickerOpen {
		return m.handleModelPickerClick(msg)
	}
	if m.checkpointsPickerOpen {
		return m.handleCheckpointsPickerClick(msg)
	}
	if m.skillsPickerOpen {
		return m.handleSkillsPickerClick(msg)
	}
	if m.memoryPickerOpen {
		return m.handleMemoryPickerClick(msg)
	}
	if m.codeMapPickerOpen {
		return m.handleCodeMapPickerClick(msg)
	}
	if m.effortPickerOpen {
		return m.handleEffortPickerClick(msg)
	}
	if m.mcpPickerOpen {
		return m.handleMCPPickerClick(msg)
	}
	if m.recallPickerOpen {
		return m.handleRecallPickerClick(msg)
	}
	if m.routerPickerOpen {
		return m.handleRouterPickerClick(msg)
	}
	if m.providerPickerOpen {
		return m.handleProviderPickerClick(msg)
	}
	if m.sandboxPickerOpen {
		return m.handleSandboxPickerClick(msg)
	}
	if m.plansPickerOpen {
		return m.handlePlansPickerClick(msg)
	}
	if m.skillsMenuOpen {
		return m.handleSkillsMenuClick(msg)
	}
	if m.sessionsPickerOpen {
		return m.handleSessionsPickerClick(msg)
	}
	if m.subagentsPickerOpen {
		return m.handleSubagentsPickerClick(msg)
	}
	if m.themePickerOpen {
		return m.handleThemePickerClick(msg)
	}
	if m.permissionsOpen {
		return m.handlePermissionsOverlayClick(msg)
	}
	if m.embedSetupOpen {
		return m.handleEmbedSetupClick(msg)
	}
	if m.anyOverlayOpen() {
		return m, nil // the 6 any-key-dismiss static panels (cheatsheet, loop-list, usage, experimental, help, context-report) — already dispatched above via dismissStaticPopup
	}
	if !m.enteredConversation {
		return m, nil // launch hero has no scrollable transcript to select
	}
	footerHeight := lipgloss.Height(m.renderFooter())
	transcriptHeight := m.height - footerHeight
	if msg.Y < transcriptHeight {
		return m.beginTranscriptSelection(msg)
	}
	m.clearTranscriptSelection()
	if line, col, ok := m.resolveInputClick(msg.X, msg.Y); ok {
		setTextInputCursor(&m.textInput, line, col)
	}
	return m, nil
}

// handleMouseMotion extends an in-progress transcript selection. A
// popup opening mid-drag (reachable only via a non-mouse path, e.g.
// Enter on a typed slash command — popups intercept clicks first, so a
// drag can't start under one) clears the selection instead of
// continuing to extend it invisibly behind the popup.
func (m Model) handleMouseMotion(msg tea.MouseMotionMsg) (Model, tea.Cmd) {
	if m.popupOpen() || m.paletteOpen || m.filePaletteOpen {
		return m.handleMouseHover(msg)
	}
	if !m.transcriptSelecting {
		return m, nil
	}
	return m.extendTranscriptSelection(msg)
}

// handleMouseHover moves the active selector for scoped interactive surfaces.
// It only runs while yottacode has intentionally enabled all-motion reporting
// for a popup or inline palette; ordinary conversation hover remains native to
// the terminal so context menus and copy/paste are not intercepted.
func (m Model) handleMouseHover(msg tea.MouseMotionMsg) (Model, tea.Cmd) {
	if m.transcriptSelecting {
		m.clearTranscriptSelection()
	}
	if m.paletteOpen {
		return m.handleSlashPaletteHover(msg), nil
	}
	if m.filePaletteOpen {
		return m.handleFilePaletteHover(msg), nil
	}
	return m.handlePopupHover(msg), nil
}

// handleSlashPaletteHover maps a mouse position over the inline slash palette
// to the same highlighted row the Up/Down keys would select.
func (m Model) handleSlashPaletteHover(msg tea.MouseMotionMsg) Model {
	if !m.paletteOpen || len(m.paletteFiltered) == 0 {
		return m
	}
	row, ok := m.inlinePaletteRow(msg.Y, len(m.paletteFiltered), slashPaletteVisible, m.paletteOffset)
	if !ok {
		return m
	}
	m.paletteIndex = m.paletteOffset + row
	return m
}

// handleFilePaletteHover maps a mouse position over the inline @file palette
// to the same highlighted row the Up/Down keys would select.
func (m Model) handleFilePaletteHover(msg tea.MouseMotionMsg) Model {
	if !m.filePaletteOpen || len(m.filePaletteFiltered) == 0 {
		return m
	}
	row, ok := m.inlinePaletteRow(msg.Y, len(m.filePaletteFiltered), filePaletteVisible, m.filePaletteOffset)
	if !ok {
		return m
	}
	m.filePaletteIndex = m.filePaletteOffset + row
	return m
}

// inlinePaletteRow resolves a screen row to a visible row inside the palette
// stacked above the input frame. The rendered palettes have a top border, a
// title, a divider, and optional overflow hint rows before selectable rows.
func (m Model) inlinePaletteRow(screenY, total, visible, offset int) (int, bool) {
	paletteTop := m.inlinePaletteTop()
	if total > visible && offset > 0 {
		paletteTop++
	}
	row := screenY - paletteTop - 3 // top border, title, and divider are non-selectable
	visibleRows := min(total, visible)
	if row < 0 || row >= visibleRows {
		return 0, false
	}
	return row, true
}

// inlinePaletteTop returns the screen row where the slash/file palette's top
// border is rendered. The hero layout is top-anchored above the first message;
// conversation layout palettes live at the top of the footer.
func (m Model) inlinePaletteTop() int {
	if !m.enteredConversation {
		box := renderStartupBox(m.version, m.commit, m.dirty, m.modelName, m.cwd, m.sess.ID, m.branch, m.memorySummary, m.providerProfile, m.startupTip(), m.width)
		return 1 + lipgloss.Height(box) + 1
	}
	paletteTop := m.height - lipgloss.Height(m.renderFooter())
	if m.turnActive {
		// The live-turn footer owns a separator, two preview rows, and a
		// spacer before aboveInputRows renders palette content.
		paletteTop += 4
	} else if preview := m.renderReasoningPreview(); preview != "" {
		paletteTop += lipgloss.Height(preview) + 1
	}
	return paletteTop
}

// handlePopupHover dispatches hover movement to the open modal or picker using
// the same hit regions as click handling, but only moves cursors; it never
// synthesizes Enter or commits an action.
func (m Model) handlePopupHover(msg tea.MouseMotionMsg) Model {
	if m.awaitingPathTrust || m.awaitingApproval {
		return m
	}
	if m.loopExitConfirmOpen {
		hits := &pickerHits{}
		box := popupBox(renderLoopExitConfirm(m, hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.loopExitConfirmCursor = index
		}
		return m
	}
	if m.modelPickerOpen && m.modelPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderModelPicker(m.modelPicker, m.popupWidth(), hits))
		if kind, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok && kind == hitItem {
			m.modelPicker.cursor = index
			m.modelPicker.clampCursorToWindow()
		}
		return m
	}
	if m.checkpointsPickerOpen && m.checkpointsPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderCheckpointsPicker(m.checkpointsPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			if m.checkpointsPicker.picked != nil {
				m.checkpointsPicker.actionIdx = index
			} else {
				m.checkpointsPicker.cursor = index
			}
		}
		return m
	}
	if m.skillsPickerOpen && m.skillsPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderSkillsPicker(m.skillsPicker, m.popupWidth(), hits))
		if kind, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			switch kind {
			case hitTab:
				if m.skillsPicker.busy == "" && !m.skillsPicker.filterMode {
					m.skillsPicker.tab = skillsCatalogTab(index)
					m.skillsPicker.cursor = 0
					m.skillsPicker.status = ""
				}
			case hitItem:
				m.skillsPicker.cursor = index
			}
		}
		return m
	}
	if m.memoryPickerOpen && m.memoryPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderMemoryPicker(m.memoryPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			if m.memoryPicker.mode == memoryBrowseMode {
				m.memoryPicker.entryCursor = index
			} else {
				m.memoryPicker.cursor = index
			}
		}
		return m
	}
	if m.codeMapPickerOpen && m.codeMapPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderCodeMapPicker(m.codeMapPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.codeMapPicker.cursor = index
		}
		return m
	}
	if m.effortPickerOpen && m.effortPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderEffortPicker(m.effortPicker, hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.effortPicker.cursor = index
		}
		return m
	}
	if m.mcpPickerOpen && m.mcpPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderMCPPicker(m.mcpPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			if m.mcpPicker.mode == mcpMenuMode {
				m.mcpPicker.menuCursor = index
			} else {
				m.mcpPicker.listCursor = index
			}
		}
		return m
	}
	if m.recallPickerOpen && m.recallPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderRecallPicker(m.recallPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.recallPicker.cursor = index
		}
		return m
	}
	if m.routerPickerOpen && m.routerPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderRouterPicker(m.routerPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			if m.routerPicker.selecting != "" {
				m.routerPicker.modelCursor = index
				m.routerPicker.clampWindow()
			} else {
				m.routerPicker.cursor = index
			}
		}
		return m
	}
	if m.providerPickerOpen && m.providerPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderProviderPicker(m.providerPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			switch m.providerPicker.mode {
			case providerMenuMode:
				m.providerPicker.menuCursor = index
			case providerUsePickerMode, providerRemovePickerMode:
				m.providerPicker.usePickerCursor = index
			case providerAddKindMode:
				m.providerPicker.addKindCursor = index
			}
		}
		return m
	}
	if m.sandboxPickerOpen && m.sandboxPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderSandboxPicker(m.sandboxPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok && sandboxMode(index) != m.sandboxPicker.cursor {
			m.sandboxPicker.cursor = sandboxMode(index)
			m.sandboxPicker.confirming = false
		}
		return m
	}
	if m.plansPickerOpen && m.plansPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderPlansPicker(m.plansPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.plansPicker.cursor = index
		}
		return m
	}
	if m.skillsMenuOpen && m.skillsMenu != nil {
		hits := &pickerHits{}
		box := popupBox(renderSkillsMenu(m.skillsMenu, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			if m.skillsMenu.mode == skillsMenuUninstallPick {
				m.skillsMenu.uninstallCursor = index
			} else {
				m.skillsMenu.cursor = index
			}
		}
		return m
	}
	if m.sessionsPickerOpen && m.sessionsPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderSessionsPicker(m.sessionsPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			switch m.sessionsPicker.mode {
			case sessionsMenuMode:
				m.sessionsPicker.menuCursor = index
			case sessionsLoadListMode, sessionsRenameListMode, sessionsExportListMode:
				m.sessionsPicker.listCursor = index
			}
		}
		return m
	}
	if m.subagentsPickerOpen && m.subagentsPicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderSubagentsPicker(m.subagentsPicker, m.popupWidth(), hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.subagentsPicker.cursor = index
			m.subagentsPicker.status = ""
		}
		return m
	}
	if m.themePickerOpen && m.themePicker != nil {
		hits := &pickerHits{}
		box := popupBox(renderThemePicker(m.themePicker, m.popupWidth(), hits))
		if kind, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok && (kind == hitItem || kind == hitTab) {
			m.themePicker.cursor = index
			m = applyHighlightedTheme(m)
		}
		return m
	}
	if m.permissionsOpen {
		hits := &pickerHits{}
		box := popupBox(renderPermissionsOverlay(m, hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.permissionsCursor = index
		}
		return m
	}
	if m.embedSetupOpen {
		hits := &pickerHits{}
		box := popupBox(m.renderEmbedSetup(hits))
		if _, index, _, ok := m.resolvePopupHover(box, hits, msg.X, msg.Y); ok {
			m.embedSetupCursor = index
		}
	}
	return m
}

// resolvePopupHover converts a screen coordinate into a registered picker hit
// for the already-rendered popup body.
func (m Model) resolvePopupHover(box string, hits *pickerHits, x, y int) (hitKind, int, string, bool) {
	ox, oy := m.popupOrigin(box)
	row, col, ok := bodyPoint(box, ox, oy, x, y)
	if !ok {
		return 0, 0, "", false
	}
	kind, index, key, ok := hits.resolve(row, col)
	if !ok || kind == hitHotkey {
		return 0, 0, "", false
	}
	return kind, index, key, true
}

// handleMouseRelease finalizes an in-progress transcript selection,
// copying the selected text to the clipboard. Updates the head point
// from the release's own coordinates first — a terminal isn't
// guaranteed to fire a motion event at the exact release position, so
// finalizing against only the last motion update could clip the final
// pixel of a fast drag.
func (m Model) handleMouseRelease(msg tea.MouseReleaseMsg) (Model, tea.Cmd) {
	if !m.transcriptSelecting {
		return m, nil
	}
	if line, col, ok := m.screenToContentPoint(msg.X, msg.Y); ok {
		m.transcriptSelectionHeadLine, m.transcriptSelectionHeadCol = line, col
	}
	return m.finalizeTranscriptSelection()
}

// handleMouseWheel routes wheel events by screen region: over the cmdline it
// browses input history (same as Up/Down at the textarea edge), and over the
// transcript it scrolls the owned viewport. Approval modals are an exception to
// the usual popup capture rule: their full preview/body lives in transcript
// history, so the wheel must keep scrolling that history while the modal is open.
func (m Model) handlePopupWheel(msg tea.MouseWheelMsg) Model {
	switch msg.Button {
	case tea.MouseWheelUp:
		return m.scrollPopupLines(-popupMouseWheelLines)
	case tea.MouseWheelDown:
		return m.scrollPopupLines(popupMouseWheelLines)
	default:
		return m
	}
}

func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (Model, tea.Cmd) {
	if m.paletteOpen || m.filePaletteOpen || m.dockFocused {
		return m, nil
	}
	if m.popupOpen() && !m.awaitingApproval {
		return m.handlePopupWheel(msg), nil
	}
	if !m.enteredConversation {
		return m, nil
	}
	footerHeight := lipgloss.Height(m.renderFooter())
	transcriptHeight := m.height - footerHeight
	if msg.Y >= transcriptHeight {
		switch msg.Button {
		case tea.MouseWheelUp:
			if out, ok := m.historyBack(); ok {
				return out, nil
			}
		case tea.MouseWheelDown:
			if out, ok := m.historyForward(); ok {
				return out, nil
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.transcriptViewport, cmd = m.transcriptViewport.Update(msg)
	m.updateTranscriptFollowIntent()
	return m, cmd
}
