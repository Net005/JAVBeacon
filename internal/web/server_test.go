package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/store"
	buildversion "github.com/Net005/JAVBeacon/internal/version"
)

func TestVersionEndpointReturnsApplicationVersion(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	expected := fmt.Sprintf(`"version":%q`, buildversion.Current())
	if !strings.Contains(rec.Body.String(), expected) {
		t.Fatalf("response = %s, want %s", rec.Body.String(), expected)
	}
}

func TestBrowserSearchEndpointServesApplicationShell(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=ABP-123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="releasesView"`) {
		t.Fatal("browser search endpoint did not serve the application shell")
	}
}

func TestOpenSearchDescriptorUsesForwardedHTTPSHost(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/opensearch.xml", nil)
	req.Host = "jav.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/opensearchdescription+xml") {
		t.Fatalf("content type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), `template="https://jav.example.test/search?q={searchTerms}"`) {
		t.Fatalf("descriptor = %s", rec.Body.String())
	}
}

func TestOpenSearchDescriptorIsAvailableWithoutAuthentication(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	handler := s.security(s.mux)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/opensearch.xml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestEmbeddedFrontendIncludesGlobalZoomAndLocalScreenshotUI(t *testing.T) {
	javascript, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`Zoom level<select id="uiZoom">`,
		`root.zoom=uiScale===100?'':String(uiScale/100)`,
		`saveDeviceDisplayPreference('uiZoom',uiZoom.value)`,
		`deviceDisplayPreferenceConfig={coverZoom:`,
		`localStorage.setItem(` + "`javbeacon.device.${name}`" + `,String(next))`,
		`function preferencePayload(){return{...prefs,...serverDisplayDefaults}}`,
		`for(const name of Object.keys(deviceDisplayPreferenceConfig))delete state[name]`,
		`toast('Release filters cleared · release-day range kept')`,
		`saveDeviceDisplayPreference('notificationCoverZoom',e.target.value)`,
		`saveDeviceDisplayPreference('monitoredCoverZoom',e.target.value)`,
		`saveDeviceDisplayPreference('downloadCoverZoom',e.target.value)`,
		`saveDeviceDisplayPreference('screenshotSlideSeconds',screenshotSlideSeconds.value)`,
		`saveDeviceDisplayPreference('releaseDetailSlideshowDelaySeconds',releaseDetailSlideshowDelaySeconds.value)`,
		`saveDeviceDisplayPreference('releaseDetailSlideshowSeconds',releaseDetailSlideshowSeconds.value)`,
		`screenshotSlideSeconds:2.5`,
		`releaseFiltersOpen:false`,
		`Number(prefs.screenshotSlideSeconds)||2.5`,
		`/releases/${release.id}/screenshots`,
		`const screenshotLightbox=$('#screenshotLightbox')`,
		`screenshotLightboxPrev=screenshotLightbox.querySelector('.screenshotLightboxPrev')`,
		`screenshotLightboxNext=screenshotLightbox.querySelector('.screenshotLightboxNext')`,
		`class="detailScreenshotNav prev"`,
		`e.button!==1`,
		`screenshotLightboxStrip.innerHTML=`,
		`e.target===screenshotLightboxInner||e.target===screenshotLightboxStage`,
		`shortcutMatches('nextItem',e.key)`,
		`safe=(v='')=>{v=String(v||'').trim();if(!v)return'';`,
		`x.download_source_reference||''`,
		`cardScreenshotTimers.get(cover)!==state||!state.indexes.length`,
		`releasedStartDate:prefs.releasedStartDate||''`,
		`function downloadNextRunText(`,
		`validDate(j.finished_at)?fullDateTime(j.finished_at):'never'`,
		`const settingsSaveStatus=$('#settingsSaveStatus')`,
		`toast(` + "`" + `Settings not saved: ${message}` + "`" + `)`,
		`'job_priority_screenshot_backfill'`,
		`Basic · interval + start time`,
		`Advanced · weekdays + time + interval`,
		`Power user · cron expression`,
		"validDate(j.finished_at)?`Last scan: ${fullDateTime(j.finished_at)}${counts}`:'Not scanned yet.'",
		`function cardMetadataGroup(role,category,values)`,
		`function cardMetadataIcon(role)`,
		`aria-label="${attr(role)}"`,
		`${filterButton(x.label,'Label')||'—'}`,
		`class="stashSyncProgress"`,
		`j.current_item?`,
		`function syncReleaseTabControls()`,
		`releaseFiltersPanel.addEventListener('toggle',()=>{if(releaseFiltersProgrammaticOpen===releaseFiltersPanel.open)`,
		`function syncReleaseFiltersFold(){const open=!releaseFiltersFoldMedia.matches||!!prefs.releaseFiltersOpen;`,
		`function applyTemporaryLibrarySearch()`,
		`location.pathname!=='/search'`,
		`get('q')`,
		`if(!applyTemporaryLibrarySearch())applyTemporaryMetadataFilter()`,
		`function savePreferences(){const payload=preferencePayload();`,
		`function updateReleaseFiltersSummary()`,
		`viewportBottom>=pageHeight*.60`,
		`releaseNavIDs.length-idx-1>25`,
		`releaseNavFromGrid?null:releaseNavIDs`,
		`function addReleaseMediaIndicators()`,
		`class="releaseNavPosition mediaPosition"`,
		`class="detailScreenshotPosition mediaPosition"`,
		`screenshotPosition.textContent=` + "`" + `${position+1} of ${state.indexes.length}` + "`" + ``,
		`screenshotLightboxCount.textContent=` + "`" + `${state.position+1} of ${count}` + "`" + ``,
		`position=releaseDetail.querySelector('.releaseNavPosition')`,
		`selected.find(s=>Number(s.site_id)===Number(site.id))`,
		`x.site_group_schedules=JSON.stringify(siteGroupSchedulesFromForm())`,
		`data-sgs-forecast`,
		`Number(row.site_group_schedule_id)===id`,
		`slice(0,3)`,
		`function focusReleaseCard(id)`,
		`returnToGrid=releaseNavFromGrid&&!releasesView.hidden`,
		`Values above 500 are supported`,
		`j.all_pages?'discovering online end':'configured limit'`,
		`cards:'true'`,
		`page.next_cursor||''`,
		`releaseCountAbort?.abort()`,
		`metadataOptionTimer=setTimeout(run,220)`,
		`function wireTouchSwipe(`,
		`el.addEventListener('pointermove'`,
		`lockReleaseBackgroundScroll()`,
		`releaseDialog.addEventListener('close',()=>{unlockReleaseBackgroundScroll();stopDetailScreenshots();`,
		`const interactive=e=>e.target.closest?.('button,a,input,select,textarea,.detailScreenshotRail,.screenshotLightboxStrip')`,
		`if(dx<0)next?.();else previous?.()`,
		`function edgeTapDirection(x,width){return x<=width*.2?-1:x>=width*.8?1:0}`,
		`wireTouchSwipe(layout,{next:()=>navigateRelease(1),previous:()=>navigateRelease(-1)`,
		`wireTouchSwipe(screenshotLightboxInner`,
		`tap:({x,width})=>runEdgeTap(x,width,navigateScreenshot)`,
		`function wireReleaseDetailTouchNavigation()`,
		`function toggleDetailScreenshots(art)`,
		`if(e.pointerType==='touch')return`,
		`if(pointerType!=='touch')return false`,
		`toggleDetailScreenshots(tappedArt)`,
		`class="mobileCoverNav prev"`,
		`mobilePrev=releaseDetail.querySelector('.mobileCoverNav.prev')`,
		`screenshotLightbox.querySelector('.screenshotLightboxClose').onclick=closeScreenshotLightbox`,
		`$('#releaseMobileClose').onclick=closeReleaseDetails`,
		`function lockDesktopReleaseScroll(){return matchMedia('(min-width:651px) and (pointer:fine)').matches}`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Fatalf("embedded app.js is missing %q", marker)
		}
	}
	if count := strings.Count(string(javascript), "settingsForm.onsubmit="); count != 1 {
		t.Fatalf("settings form has %d submit handlers, want one atomic handler", count)
	}
	for _, marker := range []string{
		`function releaseDocumentTitle(x)`,
		`parts.filter(Boolean).join(' · ')+' — JAVBeacon'`,
		`if(activeReleaseID!==id)return`,
		`document.title=releaseDocumentTitle(x)`,
		`activeReleaseData=null;document.title='JAVBeacon'`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Fatalf("Release Details browser-title behavior is missing %q", marker)
		}
	}
	for _, marker := range []string{
		`function downloadPillTelemetry(x)`,
		`download_eta_seconds`,
		`download_seeds`,
		`download_peers`,
		`download_seen_complete`,
		`download_added_at`,
		`class="downloadPillWrap"`,
		`const x=await api('/releases/'+id)`,
		`function closeDownloadTelemetry(except=null)`,
		`wrap.classList.toggle('telemetryOpen',opening)`,
		`e.stopImmediatePropagation()`,
		`function actionIcon(kind)`,
		`function decorateActionButtons(root=document)`,
		`actionIcon('watchlist')`,
		`actionIcon('notification')`,
		`actionIcon('monitor')`,
		`class="releaseStatusInfo downloaded"`,
		`class="releaseStatusInfo local"`,
		`className='actionGroup statusGroup'`,
		`status.innerHTML='<div class="actionGroupTitle">Status</div><div class="statusItems"></div>'`,
		`function detailFilterList(values,category)`,
		`function syncDetailValueOverflow(root=releaseDetail)`,
		`class="detailValueMore"`,
		`function detailSiteLinks(x)`,
		`siteTerm.textContent='Sites'`,
		`function applyMonitoringSiteTarget()`,
		`data-site-id="${x.id}"`,
		`scrollIntoView({behavior:'smooth',block:'center'})`,
		`releaseDetail.querySelector('.detailStory')?.remove()`,
		`actionIcon('search')+'<span>+ Download</span>'`,
		`detailFilterList([x.studio],'Studio')`,
		`detailFilterList([x.label],'Label')`,
		`function initializeAppHistory()`,
		`function closeReleaseDetails()`,
		`window.addEventListener('popstate'`,
		`releaseID:id,releaseDepth}`,
		`history.go(-releaseDepth)`,
		`title:'Scan the new monitoring site?'`,
		`mode:'full',pages:0,all_pages:true,kind:'manual_full'`,
		`function downloadSortTab(){return downloadStatus==='downloading'?'downloading':'other'}`,
		`['eta','ETA'],['progress','Percentage']`,
		`function rememberDownloadSort()`,
		`releaseToastNode.className='releaseToast'`,
		`searchDialogToastNode.className='releaseToast searchDialogToast'`,
		`searchDialog?.open?searchDialogToastNode`,
		`function patchNotificationReleaseState(id,kind,value)`,
		`class="stateButton compactActionMenu"`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Fatalf("Release Details download telemetry is missing %q", marker)
		}
	}
	if strings.Contains(string(javascript), "These options search for and redownload a copy.") {
		t.Fatal("Release Details still includes the redundant redownload explanation")
	}
	if strings.Contains(string(javascript), "restoreToastHost") {
		t.Fatal("Release Details notifications must not reparent the global toast")
	}
	if strings.Contains(string(javascript), "function closeReleaseDetails(){if(history.state?.javbeacon&&history.state.releaseID){history.back()") {
		t.Fatal("explicitly closing Release Details must not step back through earlier release entries")
	}
	script := string(javascript)
	loadAll := strings.Index(script, "async function loadAll()")
	if loadAll < 0 {
		t.Fatal("embedded app.js is missing startup orchestration")
	}
	loadSites := strings.Index(script[loadAll:], "loadSites()")
	loadSettings := strings.Index(script[loadAll:], "loadSettings()")
	if loadSites < 0 || loadSettings < 0 || loadSites >= loadSettings {
		t.Fatal("startup must load monitoring sites before rendering site group schedules")
	}
	serializerStart := strings.Index(script, "function siteGroupSchedulesFromForm()")
	if serializerStart < 0 {
		t.Fatal("embedded app.js is missing the site group schedule serializer")
	}
	serializerEnd := strings.Index(script[serializerStart:], "async function loadSites()")
	if serializerEnd < 0 {
		t.Fatal("embedded app.js is missing the site group schedule serializer")
	}
	serializer := script[serializerStart : serializerStart+serializerEnd]
	if strings.Contains(serializer, "if(!chosenSites.length)return null") {
		t.Fatal("site group schedule serializer must not silently discard schedules without rendered site choices")
	}
	clearStart := strings.Index(string(javascript), "function clearReleaseFilterState()")
	clearEnd := strings.Index(string(javascript), "async function loadPresets()")
	if clearStart < 0 || clearEnd <= clearStart {
		t.Fatal("embedded app.js is missing the Release Library filter reset")
	}
	clearReleaseFilters := string(javascript)[clearStart:clearEnd]
	if strings.Contains(clearReleaseFilters, "prefs.releasedMinDays=''") || strings.Contains(clearReleaseFilters, "prefs.releasedMaxDays=''") {
		t.Fatal("Release Library filter reset must preserve the days-since-release range")
	}
	stylesheet, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`.detailScreenshotRail{`, `.detailScreenshotNav{`, `height:134px`, `.screenshotLightboxInner{`, `.screenshotLightboxStrip button.active{`, `backdrop-filter:blur(10px)`, `.releasedRangeControls{`, `.releaseFiltersPanel>summary{display:none}`, `.releaseFiltersPanel:not([open])>.releaseFiltersBody{display:none!important}`, `.cardMetadataRole{`, `.stashSyncProgress{`, `.sidebarFooter{display:grid;grid-template-columns:max-content minmax(0,1fr)`, `.sidebarFooter #appVersion{display:block;min-width:max-content`, `#toast[popover]{position:fixed!important;inset:auto 22px 22px auto!important`, `.card.returnFocus{`, `.titleRow{align-items:baseline}`, `.downloadToolbar{align-items:flex-end;`, `grid-template-columns:repeat(8,minmax(0,1fr))!important`, `.releaseVisual{display:grid!important;grid-template-rows:auto auto!important`, `.releaseArt .detailBackdrop{display:none!important}`, `.screenshotLightboxStage{touch-action:pan-y`, `.releaseDialog .releaseChrome{display:none!important}`, `.mobileCoverNav{`, `.releaseLayout,.screenshotLightboxInner{`, `-webkit-user-select:none;user-select:none;-webkit-touch-callout:none`, `touch-action:pan-y pinch-zoom;overscroll-behavior-x:contain`, `.releaseDialog button,.screenshotLightbox button{`, `touch-action:manipulation;-webkit-tap-highlight-color:transparent`, `.screenshotLightboxClose{`, `.screenshotLightboxClose{width:104px;height:104px;font-size:48px}`, `.screenshotLightboxClose{width:120px;height:120px;font-size:56px}`, `top:max(10px,env(safe-area-inset-top))!important;right:max(10px,env(safe-area-inset-right))!important`, `(orientation:landscape) and (pointer:coarse)`, `(max-width:1366px) and (pointer:coarse)`, `width:120px;height:120px`, `width:104px;height:104px`, `width:clamp(48px,6vw,64px)!important`, `top:max(8px,env(safe-area-inset-top))!important;right:max(8px,env(safe-area-inset-right))!important`, `width:var(--vw100,100vw)!important;max-width:var(--vw100,100vw)!important`, `overflow-y:auto!important;overscroll-behavior:contain!important`, `body.releaseOverlayOpen{position:fixed!important`} {
		if !strings.Contains(string(stylesheet), marker) {
			t.Fatalf("embedded app.css is missing %q", marker)
		}
	}
	if !strings.Contains(string(stylesheet), `.releaseArt .detailCover{color:transparent;font-size:0;text-indent:-9999px}`) {
		t.Fatal("Release Details cover must hide image fallback text while navigation loads the next cover")
	}
	for _, marker := range []string{`.downloadPillTelemetry{`, `.downloadPillWrap:hover .downloadPillTelemetry`, `.downloadPillWrap.telemetryOpen .downloadPillTelemetry`, `.detailActionGroups:has(.downloadPillWrap)`, `grid-template-columns:repeat(2,minmax(118px,1fr))`, `.actionIcon svg{`} {
		if !strings.Contains(string(stylesheet), marker) {
			t.Fatalf("Release Details download telemetry styling is missing %q", marker)
		}
	}
	for _, marker := range []string{`.releaseStatusInfo{`, `.releaseStatusInfo.downloaded{`, `.releaseStatusInfo.local{`, `.releaseToast{`, `.releaseToast.show{`, `.statusItems .downloadPill.inline{width:100%;min-height:34px`, `.statusItems .releaseStatusInfo b{font-size:11px}`} {
		if !strings.Contains(string(stylesheet), marker) {
			t.Fatalf("Release Details status/notification styling is missing %q", marker)
		}
	}
	for _, marker := range []string{`.releaseStates,.notificationCard .releaseStates{grid-template-columns:repeat(3,minmax(0,1fr)) auto}`, `.releaseStates>.cardMenu>.compactActionMenu{`} {
		if !strings.Contains(string(stylesheet), marker) {
			t.Fatalf("compact cover-card actions styling is missing %q", marker)
		}
	}
	markup, err := assets.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markup), `aria-label="Fullscreen screenshot view"`) {
		t.Fatal("embedded index.html is missing the fullscreen screenshot view")
	}
	for _, marker := range []string{`data-watchlist="true">Watchlist`, `Added to Watchlist`, `<option value="added">Date added</option><option value="updated">Date updated</option>`} {
		if !strings.Contains(string(markup), marker) {
			t.Fatalf("embedded index.html is missing Watchlist/sort terminology %q", marker)
		}
	}
	if !strings.Contains(string(markup), `id="releaseMobileClose"`) || !strings.Contains(string(markup), `<span aria-hidden="true">×</span>`) {
		t.Fatal("embedded index.html is missing the mobile release close target")
	}
	if !strings.Contains(string(markup), `id="releasedStartDate" type="date"`) {
		t.Fatal("embedded index.html is missing the persisted Released-tab start date")
	}
	if !strings.Contains(string(markup), `id="releaseFiltersPanel" class="releaseFiltersPanel" open`) || !strings.Contains(string(markup), `id="releaseFiltersSummary"`) {
		t.Fatal("embedded index.html is missing the mobile Release Library filter fold")
	}
	if !strings.Contains(string(markup), `rel="search" type="application/opensearchdescription+xml" href="/opensearch.xml"`) {
		t.Fatal("embedded index.html is missing Firefox/OpenSearch discovery")
	}
	loginMarkup, err := assets.ReadFile("static/login.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loginMarkup), `rel="search" type="application/opensearchdescription+xml" href="/opensearch.xml"`) {
		t.Fatal("embedded login.html is missing Firefox/OpenSearch discovery")
	}
	if strings.Contains(string(markup), `<p class="eyebrow">SERVER DIAGNOSTICS</p><h2>Live application log</h2>`) || strings.Contains(string(markup), `<p class="eyebrow">STASHAPP RECOVERY</p><h2>Missing library files</h2>`) {
		t.Fatal("embedded index.html still contains duplicate Logs or Missing Files page headings")
	}
	if strings.Count(string(markup), `class="sectionHead compactViewHead"`) < 2 {
		t.Fatal("embedded index.html is missing compact Logs and Missing Files headers")
	}
	for _, capped := range []string{
		`name="pages" type="number" min="1" max="500"`,
		`name="page_limit" type="number" min="1" max="500"`,
		`name="full_refresh_page_limit" type="number" min="1" max="500"`,
	} {
		if strings.Contains(string(markup), capped) {
			t.Fatalf("embedded index.html still caps scrape pages: %s", capped)
		}
	}
	if strings.Contains(string(javascript), `name="new_release_refresh_page_limit" type="number" min="1" max="500"`) {
		t.Fatal("embedded app.js still caps New Release Only scrape pages")
	}
}

func TestEmbeddedFrontendLiveReleaseUpdatesReloadActiveQuery(t *testing.T) {
	javascript, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`function scheduleReleaseStreamReload()`,
		`if(releasesView.hidden)return`,
		`handleStreamRelease(x.release)`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Fatalf("embedded app.js is missing filtered live-update behavior %q", marker)
		}
	}
	if strings.Contains(string(javascript), `else releases.unshift(x)`) {
		t.Fatal("embedded app.js still prepends unfiltered live releases")
	}
}

func TestEmbeddedFrontendIncludesManualHistoricalBackfillProgress(t *testing.T) {
	javascript, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"JavLibrary historical backfill", "historicalBackfillResume", "Historical overall", "/jobs/javlibrary-historical-backfill", "placeholder=\"500\"", "validDate(j.finished_at)?' · last stopped '"} {
		if !strings.Contains(string(javascript), marker) {
			t.Fatalf("embedded historical backfill UI is missing %q", marker)
		}
	}
}

func TestReleaseScreenshotManifestOnlyExposesLocalCacheFiles(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "screenshot-manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SHOT-22", Title: "Screenshots", Source: "JavLibrary", Screenshots: []string{"https://example.invalid/full.jpg"}})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "SHOT-22", Limit: 1})
	if len(releases) != 1 {
		t.Fatal("release setup failed")
	}
	cache, err := screenshots.New(t.TempDir(), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st, screenshots: cache}

	missingReq := httptest.NewRequest(http.MethodGet, "/screenshots/1/0", nil)
	missingReq.SetPathValue("id", strconv.FormatInt(releases[0].ID, 10))
	missingReq.SetPathValue("index", "0")
	missingRec := httptest.NewRecorder()
	server.screenshot(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("uncached screenshot status=%d, want 404", missingRec.Code)
	}

	path := cache.Path(releases[0].VideoID, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("local-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestReq := httptest.NewRequest(http.MethodGet, "/api/releases/1/screenshots", nil)
	manifestReq.SetPathValue("id", strconv.FormatInt(releases[0].ID, 10))
	manifestRec := httptest.NewRecorder()
	server.releaseScreenshots(manifestRec, manifestReq)
	if manifestRec.Code != http.StatusOK || !strings.Contains(manifestRec.Body.String(), `"indexes":[0]`) {
		t.Fatalf("manifest status=%d body=%s", manifestRec.Code, manifestRec.Body.String())
	}
}

func TestPendingChangelogEndpointIsAcknowledgedOnce(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "changelog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{
		"app_installed_version":      "1.0.7",
		"app_changelog_pending_from": "1.0.5",
		"app_changelog_pending_to":   "1.0.7",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, mux: http.NewServeMux()}
	s.routes()

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/changelog/pending", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":true`) || !strings.Contains(rec.Body.String(), `"version":"1.0.6"`) {
		t.Fatalf("pending response: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body := strings.NewReader(`{"from":"v1.0.5","to":"v1.0.7"}`)
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/changelog/acknowledge", body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"acknowledged":true`) {
		t.Fatalf("acknowledge response: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/changelog/pending", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("second pending response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCoverCacheJobCachesMissingAndSkipsExistingCovers(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "covers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("test-cover"))
	}))
	defer imageServer.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Test", Source: "JavLibrary", ImageURL: imageServer.URL})
	cache, err := covers.New(filepath.Join(t.TempDir(), "cache"), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, covers: cache, log: slog.Default()}

	if err := s.startCoverCache(ctx); err != nil {
		t.Fatal(err)
	}
	first := waitForCoverJob(t, s)
	if first.Total != 1 || first.Checked != 1 || first.Cached != 1 || first.Failed != 0 {
		t.Fatalf("first cover job: %+v", first)
	}
	if _, err := os.Stat(cache.Path("TEST-1")); err != nil {
		t.Fatalf("cached cover: %v", err)
	}

	if err := s.startCoverCache(ctx); err != nil {
		t.Fatal(err)
	}
	second := waitForCoverJob(t, s)
	if second.Checked != 1 || second.Cached != 0 || second.Skipped != 1 || second.Failed != 0 {
		t.Fatalf("second cover job: %+v", second)
	}
}

func TestCoverEndpointServesBrandedPlaceholderWhenArtworkIsUnavailable(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "placeholder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PENDING-1", Title: "Pending", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PENDING-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(releases), err)
	}

	s := &Server{store: st, log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/covers/1", nil)
	req.SetPathValue("id", strconv.FormatInt(releases[0].ID, 10))
	rec := httptest.NewRecorder()
	s.cover(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "image/svg+xml") {
		t.Fatalf("content type = %q, want SVG", contentType)
	}
	if !strings.Contains(rec.Body.String(), "Cover not yet available") || !strings.Contains(rec.Body.String(), "JAVBEACON") {
		t.Fatal("response did not contain the branded unavailable-cover artwork")
	}
}

// TestDownloadListReturnsPaginatedItemsAndTotal covers Phase 4B: the
// /api/downloads response shape changed from a bare array to {items,total}
// so the Download Activity table can paginate against a true total.
func TestDownloadListReturnsPaginatedItemsAndTotal(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DL-1", Title: "DL-1", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "DL-1"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	releaseID := all[0].ID
	for _, x := range []domain.Download{
		{ReleaseID: releaseID, Query: "Q1", Status: "downloading"},
		{ReleaseID: releaseID, Query: "Q2", Status: "downloading"},
		{ReleaseID: releaseID, Query: "Q3", Status: "completed"},
	} {
		if _, err := st.SaveDownload(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads?status=downloading&limit=1", nil))
	var body struct {
		Items []domain.Download `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Items) != 1 {
		t.Fatalf("body=%+v, want total=2 items=1", body)
	}
}

func TestReleaseDetailsIncludesLatestDownloadTelemetry(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "release-download-telemetry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ETA-1", Title: "Download telemetry", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ETA-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(releases), err)
	}
	releaseID := releases[0].ID
	if _, err = st.SaveDownload(ctx, domain.Download{ReleaseID: releaseID, Status: "downloading", SourceReference: "https://sukebei.nyaa.si/view/321", Seeds: 18, Peers: 7, ETASeconds: 456, SeenComplete: 1700000000}); err != nil {
		t.Fatal(err)
	}

	s := &Server{store: st, log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/api/releases/"+strconv.FormatInt(releaseID, 10), nil)
	req.SetPathValue("id", strconv.FormatInt(releaseID, 10))
	rec := httptest.NewRecorder()
	s.release(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body domain.Release
	if err = json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.DownloadStatus != "downloading" || body.DownloadSeeds != 18 || body.DownloadPeers != 7 || body.DownloadETASeconds != 456 || body.DownloadSeenComplete != 1700000000 || body.DownloadAddedAt.IsZero() {
		t.Fatalf("release download telemetry response: %+v", body)
	}
}

func TestBulkRemoveDownloadsRunsDestructiveCleanupInBackground(t *testing.T) {
	deleted := make(chan struct{}, 1)
	qbMux := http.NewServeMux()
	qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"hash":"bulkhash","name":"BULK-1 incomplete"}]`))
	})
	qbMux.HandleFunc("POST /api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("hashes") != "bulkhash" || r.FormValue("deleteFiles") != "true" {
			t.Errorf("bulk delete form hashes=%q deleteFiles=%q", r.FormValue("hashes"), r.FormValue("deleteFiles"))
		}
		deleted <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	})
	qb := httptest.NewServer(qbMux)
	defer qb.Close()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bulk-downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SaveSettings(ctx, map[string]string{"qb_url": qb.URL})
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "BULK-1", Title: "BULK-1"})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "BULK-1", Limit: 10})
	history, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "BULK-1", TorrentHash: "bulkhash", Status: "downloading"})
	s := &Server{store: st, downloads: download.New(st, time.Second, slog.Default()), log: slog.Default()}
	body, _ := json.Marshal(map[string]any{"ids": []int64{history.ID}, "replace": false})
	rec := httptest.NewRecorder()
	s.bulkRemoveDownloads(rec, httptest.NewRequest(http.MethodPost, "/api/downloads/bulk-remove", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-deleted:
	case <-time.After(2 * time.Second):
		t.Fatal("background qBittorrent delete did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := st.Downloads(ctx, "")
		if len(rows) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download history was not cleared by background bulk removal")
}

// TestDownloadListFiltersByFilenamePatternExcluded covers TODO-2.0 Task A's
// Download Activity filter: ?filename_pattern_excluded=true must restrict
// the results to downloads flagged by either the manual "Force download"
// override or the Missing Library Files non-preferred-filename fallback
// chain, and omitting the param (or any other value) must not filter at
// all - mirroring downloadList's other query-param-driven filters.
func TestDownloadListFiltersByFilenamePatternExcluded(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads-excluded.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DL-2", Title: "DL-2", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "DL-2"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	releaseID := all[0].ID
	for _, x := range []domain.Download{
		{ReleaseID: releaseID, Query: "NORMAL", Status: "downloading"},
		{ReleaseID: releaseID, Query: "EXCLUDED", Status: "downloading", FilenamePatternExcluded: true},
	} {
		if _, err := st.SaveDownload(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads?filename_pattern_excluded=true", nil))
	var body struct {
		Items []domain.Download `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Query != "EXCLUDED" {
		t.Fatalf("body=%+v, want exactly the EXCLUDED row", body)
	}

	rec = httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("body=%+v, want both rows when the filter is omitted", body)
	}
}

// TestPatchReleaseUpdatesLabel covers Phase 6B: PATCH /api/releases/{id}
// accepts a "label" field and persists it via the store.
func TestPatchReleaseUpdatesLabel(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "patch-label.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LBL-1", Title: "LBL-1", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "LBL-1"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	s := &Server{store: st, log: slog.Default()}

	req := httptest.NewRequest(http.MethodPatch, "/api/releases/"+strconv.FormatInt(all[0].ID, 10), strings.NewReader(`{"label":"MOODYZ"}`))
	req.SetPathValue("id", strconv.FormatInt(all[0].ID, 10))
	rec := httptest.NewRecorder()
	s.patchRelease(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body domain.Release
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Label != "MOODYZ" {
		t.Fatalf("response label=%q, want MOODYZ", body.Label)
	}
	got, err := st.Release(ctx, all[0].ID)
	if err != nil || got.Label != "MOODYZ" {
		t.Fatalf("persisted label=%q err=%v", got.Label, err)
	}
}

// TestPatchReleasesBulkAppliesStopMonitoringAndAllowNonPreferredFlag covers
// the "Releases checked by the scheduled job" table's mass-select bulk
// actions: PATCH /api/releases/bulk must apply monitor_download and/or
// allow_non_preferred_filenames to every id in the request in one call.
func TestPatchReleasesBulkAppliesStopMonitoringAndAllowNonPreferredFlag(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "patch-bulk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, videoID := range []string{"BULKW-1", "BULKW-2"} {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(rows), err)
	}
	monitor := true
	for _, r := range rows {
		ids = append(ids, r.ID)
		if err := st.PatchRelease(ctx, r.ID, nil, nil, nil, nil, nil, &monitor, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	body, _ := json.Marshal(map[string]any{"ids": ids, "monitor_download": false, "allow_non_preferred_filenames": true})
	req := httptest.NewRequest(http.MethodPatch, "/api/releases/bulk", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.patchReleasesBulk(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Updated int64 `json:"updated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Updated != 2 {
		t.Fatalf("expected 2 rows updated, got %+v", resp)
	}
	for _, id := range ids {
		got, err := st.Release(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.MonitorDownload {
			t.Fatalf("release %d should no longer be monitored: %+v", id, got)
		}
		if !got.AllowNonPreferredFilenames {
			t.Fatalf("release %d should have the allow-non-preferred flag set: %+v", id, got)
		}
	}
}

// TestReleasesCountEndpointMatchesReleasesFilter covers Phase 4A: the new
// /api/releases/count endpoint accepts the same filter params as
// /api/releases and reports the true total, ignoring limit/offset.
func TestReleasesCountEndpointMatchesReleasesFilter(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "releases.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []string{"MON-1", "MON-2", "MON-3"} {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary", MonitorDownload: true}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.releasesCount(rec, httptest.NewRequest(http.MethodGet, "/api/releases/count?monitor_download=true&limit=1", nil))
	var body struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 3 {
		t.Fatalf("total=%d, want 3 (count must ignore limit)", body.Total)
	}
}

// TestLogEntriesPaginatesWithBeforeAndAfterCursors covers Phase 13's
// incremental log loading over the actual GET /api/logs handler: no cursor
// returns the newest page, `before` pages backward (older entries,
// ascending) for infinite-scroll, and `after` returns only entries strictly
// newer than the cursor for an efficient tail-poll that does not require
// re-fetching the whole visible window on every tick.
func TestLogEntriesPaginatesWithBeforeAndAfterCursors(t *testing.T) {
	ring := logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100)
	log := slog.New(ring)
	for _, msg := range []string{"one", "two", "three", "four", "five"} {
		log.Info(msg)
	}
	s := &Server{logs: ring, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, "/api/logs?limit=2", nil))
	var newest []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&newest); err != nil {
		t.Fatal(err)
	}
	if len(newest) != 2 || newest[0].Message != "four" || newest[1].Message != "five" {
		t.Fatalf("newest page=%+v, want [four five]", newest)
	}

	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?before=%d&limit=10", newest[0].Seq), nil))
	var older []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&older); err != nil {
		t.Fatal(err)
	}
	if len(older) != 3 || older[0].Message != "one" || older[1].Message != "two" || older[2].Message != "three" {
		t.Fatalf("older page=%+v, want [one two three]", older)
	}

	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?after=%d&limit=10", newest[1].Seq), nil))
	var tail []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("tail page before any new entry=%+v, want empty", tail)
	}

	log.Info("six")
	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?after=%d&limit=10", newest[1].Seq), nil))
	if err := json.NewDecoder(rec.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0].Message != "six" {
		t.Fatalf("tail page after new entry=%+v, want [six]", tail)
	}
}

func waitForCoverJob(t *testing.T, s *Server) coverCacheStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := s.coverCacheStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cover cache job did not finish")
	return coverCacheStatus{}
}

func TestNotificationSortOptionsAndTabDefaults(t *testing.T) {
	raw, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	wantOptions := "notificationSortOptions=[['downloaded','Download date'],['download_started','Download started'],['local_available','Locally available'],['notification','Notification date'],['release','Release date']]"
	if !strings.Contains(script, wantOptions) {
		t.Fatal("notification sort options are missing or not alphabetized")
	}
	wantDefaults := "notificationDefaultSort={new_release:'release',local_available:'local_available',downloaded:'downloaded',download_started:'download_started',download_failed:'notification'}"
	if !strings.Contains(script, wantDefaults) {
		t.Fatal("notification tabs do not have the requested event-date defaults")
	}
}

// TestSettingsRejectsInvalidSiteGroupSchedules covers the site_group_schedules
// validation block added for the new per-site-group scrape schedule feature
// (see domain.SiteGroupSchedule and internal/monitor's
// expandSiteGroupSchedules). Every case here is rejected before the settings
// handler ever reaches s.covers/s.monitor, so a minimal *Server (store only)
// is enough - PUT /api/settings validates in order: JSON parses, each
// schedule has a name, has at least one site, every site has a valid
// quick/full/new mode, priority is 1-999, schedule mode is
// basic/advanced/cron, advanced requires a start time, and cron requires a
// cron expression.
func TestSettingsRejectsInvalidSiteGroupSchedules(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "site-group-schedule-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, log: slog.Default()}
	for _, c := range []struct {
		name string
		raw  string
	}{
		{"invalid json", `not json`},
		{"missing name", `[{"enabled":true,"priority":10,"sites":[{"site_id":1,"mode":"quick"}]}]`},
		{"no sites", `[{"name":"Favorites","enabled":true,"priority":10,"sites":[]}]`},
		{"bad site mode", `[{"name":"Favorites","enabled":true,"priority":10,"sites":[{"site_id":1,"mode":"bogus"}]}]`},
		{"priority out of range", `[{"name":"Favorites","enabled":true,"priority":0,"sites":[{"site_id":1,"mode":"quick"}]}]`},
		{"bad schedule mode", `[{"name":"Favorites","enabled":true,"priority":10,"schedule_mode":"weird","sites":[{"site_id":1,"mode":"quick"}]}]`},
		{"advanced without start time", `[{"name":"Favorites","enabled":true,"priority":10,"schedule_mode":"advanced","sites":[{"site_id":1,"mode":"quick"}]}]`},
		{"cron without expression", `[{"name":"Favorites","enabled":true,"priority":10,"schedule_mode":"cron","sites":[{"site_id":1,"mode":"quick"}]}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			encoded, err := json.Marshal(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"site_group_schedules":` + string(encoded) + `}`
			req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
			rec := httptest.NewRecorder()
			s.settings(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s, want 422", rec.Code, rec.Body.String())
			}
		})
	}
}
