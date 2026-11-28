import AppKit
import UserNotifications
import EventKit
import CryptoKit
import Darwin

struct Notice: Codable {
    var id: String
    var kind: String
    var title: String
    var body: String
    var url: String
    var fire: Double
    var expires: Double
}
struct SyncReview: Decodable, Equatable {
    var path: String
    var tasks: [String]?
}
struct ManifestHealth: Decodable {
    var calendar: String
    var transport: String
    var sync: String?
    var sync_review: [SyncReview]?
    var checked_at: Double? = nil
}
struct Manifest: Decodable {
    var version: Int
    var notices: [Notice]
    var health: ManifestHealth?
    var confirmed_meetings: [String]? = nil
    var meeting_checks: [String: Int]? = nil
}
struct PendingNotice {
    var id: String
    var title: String
    var body: String
    var url: String
    var fire: Double?
}
func pendingNotice(_ request: UNNotificationRequest) -> PendingNotice {
    PendingNotice(id: request.identifier, title: request.content.title, body: request.content.body,
                  url: request.content.userInfo["url"] as? String ?? "",
                  fire: (request.trigger as? UNCalendarNotificationTrigger)?.nextTriggerDate()?.timeIntervalSince1970)
}
struct RepairPlan {
    var remove: [String] = []
    var restore: Set<String> = []
    var changed = 0
}
func reconciliationDue(now: Double, checked: Double) -> Bool {
    checked <= 0 || now < checked || now - checked >= 3 * 3600
}
func meetingIsDue(_ notice: Notice, now: Double) -> Bool {
    notice.kind == "meeting" && notice.fire <= now && now < notice.expires
}
func repairPlan(wanted: [String: Notice], pending: [PendingNotice], now: Double, unavailable: [String]) -> RepairPlan {
    var plan = RepairPlan()
    for request in pending {
        if removePending(id: request.id, wanted: Set(wanted.keys), unavailablePrefixes: unavailable) {
            plan.remove.append(request.id)
        } else if let notice = wanted[request.id], notice.fire > now + 1,
                  request.title != notice.title || request.body != notice.body || request.url != notice.url || request.fire == nil || abs(request.fire! - notice.fire) > 2 {
            plan.remove.append(request.id)
            plan.restore.insert(request.id)
            plan.changed += 1
        }
    }
    return plan
}
struct Config: Decodable {
    var enabled: Bool
    var meeting_notifications: Bool
    var meeting_source: String
    var exclude_meetings: [[String: String]]
    var lacia: String
    var tasks_repo: String?
    var site: String?
}
// The pages a notification may open: the configured site only ("site" in
// config.json; without it, the server in the local environment). Plain http is accepted
// only for a loopback server.
let defaultSite = "http://127.0.0.1:10148"
func siteBase(_ site: String?) -> String {
    var base = (site ?? "").isEmpty ? defaultSite : site!
    while base.hasSuffix("/") { base.removeLast() }
    return base
}
func siteAllows(_ url: URL, site: String?) -> Bool {
    guard let base = URL(string: siteBase(site)), let scheme = url.scheme, scheme == base.scheme,
          let host = url.host, host == base.host, url.port == base.port,
          scheme == "https" || (scheme == "http" && (host == "127.0.0.1" || host == "localhost")),
          url.user == nil, url.password == nil, url.query == nil, url.fragment == nil else { return false }
    return true
}
// Keep this limited to event links accepted by calendar.SafeLink.
func calendarEventAllows(_ url: URL) -> Bool {
    guard let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
          parts.scheme == "https", let host = parts.host?.lowercased(),
          ["calendar.google.com", "www.google.com"].contains(host),
          parts.port == nil || parts.port == 443,
          parts.user == nil, parts.password == nil, parts.fragment == nil,
          parts.percentEncodedPath == "/calendar/event", let items = parts.queryItems else { return false }
    return items.contains { $0.name == "eid" } && items.allSatisfy {
        ($0.name == "eid" || $0.name == "ctz") && $0.value?.isEmpty == false
    }
}
func notificationDestination(_ value: String, id: String, site: String?) -> URL? {
    guard let url = URL(string: value) else { return nil }
    return siteAllows(url, site: site) || (id.hasPrefix("meeting-") && calendarEventAllows(url)) ? url : nil
}
func manifestNotices(_ manifest: Manifest?, config: Config, fresh: Bool) -> [Notice] {
    guard config.enabled, let manifest, manifest.version == 1 else { return [] }
    return manifest.notices.filter { notice in
        switch notice.kind {
        case "task": return true
        case "sync": return fresh
        default: return config.meeting_notifications && ["snapshot", "linked"].contains(config.meeting_source)
        }
    }
}
func canReconcileManifest(_ manifest: Manifest?, fresh: Bool) -> Bool {
    fresh && manifest?.health?.transport != "failed"
}
// The durable plan survives a failed transport or a worker restart. Only a
// usable manifest can replace it; unknown calendar results already retain
// their occurrences in the Go planner.
func meetingDeliveryPlan(_ manifest: Manifest?, config: Config, fresh: Bool, saved: [Notice], now: Double) -> [Notice] {
    guard config.enabled && config.meeting_notifications && ["snapshot", "linked"].contains(config.meeting_source) else { return [] }
    if canReconcileManifest(manifest, fresh: fresh), let manifest,
       ["ok", "partial"].contains(manifest.health?.calendar ?? "") {
        return manifestNotices(manifest, config: config, fresh: fresh).filter { $0.kind == "meeting" && $0.expires > now }
    }
    return saved.filter { $0.expires > now }
}
func dueManifestMeetings(_ manifest: Manifest?, config: Config, fresh: Bool, now: Double, saved: [Notice] = []) -> [Notice] {
    meetingDeliveryPlan(manifest, config: config, fresh: fresh, saved: saved, now: now).filter { notice in
        guard meetingIsDue(notice, now: now) else { return false }
        // Give the worker's one-minute Google request time to finish. A
        // failed/missing worker cannot suppress the original plan forever.
        return !canReconcileManifest(manifest, fresh: fresh) || manifest?.meeting_checks?[notice.id] == 1 || now >= notice.fire + 35
    }
}
func syncLabel(_ state: String?, fresh: Bool, configured: Bool) -> String {
    if !configured || (fresh && state == "disabled") { return "タスクの自動同期は無効です" }
    guard fresh else { return "タスクの同期状況を確認できません" }
    switch state {
    case "ok": return "タスクを同期できています"
    case "failed": return "タスクを同期できませんでした"
    case "checking": return "タスクの同期状況を確認しています"
    default: return "タスクの同期状況を確認できません"
    }
}
func syncBackupURL(_ path: String, repo: String?) -> URL? {
    guard let repo, repo.hasPrefix("/"), path.hasPrefix("/"),
          !path.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }) else { return nil }
    let root = URL(fileURLWithPath: repo, isDirectory: true).resolvingSymlinksInPath()
    let backupRoot = root.appendingPathComponent("local", isDirectory: true).standardizedFileURL
    guard backupRoot.path == backupRoot.resolvingSymlinksInPath().path else { return nil }
    let backup = URL(fileURLWithPath: path, isDirectory: true).resolvingSymlinksInPath()
    guard backup.pathComponents.count > backupRoot.pathComponents.count,
          backup.pathComponents.starts(with: backupRoot.pathComponents) else { return nil }
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: backup.path, isDirectory: &isDirectory), isDirectory.boolValue else { return nil }
    return backup
}
func digest(_ value: String) -> String { SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined() }
func privateWrite<T: Encodable>(_ value: T, _ url: URL) throws {
    let data = try JSONEncoder().encode(value)
    try data.write(to: url, options: .atomic)
    try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
}


func meetingCandidate(allDay: Bool, canceled: Bool, selfDeclined: Bool, otherAttendee: Bool, url: URL?) -> Bool {
    let host = url?.host?.lowercased() ?? ""
    let conference = ["meet.google.com", "zoom.us", "teams.microsoft.com"].contains { host == $0 || host.hasSuffix("." + $0) }
    return !allDay && !canceled && !selfDeclined && (otherAttendee || conference)
}
func removePending(id: String, wanted: Set<String>, unavailablePrefixes: [String]) -> Bool {
    !wanted.contains(id) && !unavailablePrefixes.contains { id.hasPrefix($0) }
}
func meetingCancellations(saved: [String: Double], accepted: [String: Double], pending: [PendingNotice],
                          wanted: Set<String>, unavailable: [String], now: Double) -> [String: Double] {
    func sourceAvailable(_ id: String) -> Bool { !unavailable.contains { id.hasPrefix($0) } }
    // Keep retrying through the original fire time, including after an app restart.
    var cancellations = saved.filter { id, fire in
        id.hasPrefix("meeting-") && fire + 86400 > now && !(wanted.contains(id) && sourceAvailable(id))
    }
    var scheduled = accepted
    for request in pending where scheduled[request.id] == nil {
        scheduled[request.id] = request.fire
    }
    for (id, fire) in scheduled where id.hasPrefix("meeting-") && fire > now && removePending(id: id, wanted: wanted, unavailablePrefixes: unavailable) {
        cancellations[id] = fire
    }
    return cancellations
}
func localExcluded(_ fields: [String: String], _ rules: [[String: String]]) -> Bool {
    rules.contains { rule in !rule.isEmpty && rule.allSatisfy { key, value in
        guard !value.isEmpty, let actual = fields[key] else { return false }
        return key == "title_contains" ? actual.contains(value) : actual == value
    }}
}

final class App: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let center = UNUserNotificationCenter.current()
    let store = EKEventStore()
    let directory = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Lacia Notifications")
    var configURL: URL { directory.appendingPathComponent("config.json") }
    var ledgerURL: URL { directory.appendingPathComponent("accepted.json") }
    var ledger: [String: Double] = [:]
    var meetingPlan: [Notice] = []
    var meetingPlanURL: URL { directory.appendingPathComponent("meeting-plan.json") }
    var cancellationURL: URL { directory.appendingPathComponent("cancelled-meetings.json") }
    var cancelledMeetings: [String: Double] = [:]
    var lastCancellationAttempt: Double = 0
    var worker: Process?
    var timer: Timer?
    var statusItem: NSStatusItem?
    var syncStatusItem: NSMenuItem?
    var syncReviewItem: NSMenuItem?
    var syncReviews: [SyncReview] = []
    var busy = false
    var lockFD: Int32 = -1
    var calendarHealth = "not_checked"
    var lastWorkerStart: Double = 0
    var terminationSignal: DispatchSourceSignal?
    var lastReconciliation: Double = 0
    var lastPlanDigest = ""
    var lastAuditAttempt: Double = 0
    var reconciliationURL: URL { directory.appendingPathComponent("reconciliation.json") }

    func applicationDidFinishLaunching(_ notification: Notification) {
        center.delegate = self
        if CommandLine.arguments.contains("--clear-pending") {
            center.removeAllPendingNotificationRequests()
            if let data = try? Data(contentsOf: ledgerURL), let saved = try? JSONDecoder().decode([String: Double].self, from: data) {
                try? privateWrite(saved.filter { $0.value <= Date().timeIntervalSince1970 }, ledgerURL)
            }
            center.getPendingNotificationRequests { _ in DispatchQueue.main.async { NSApp.terminate(nil) } }
            return
        }
        if CommandLine.arguments.contains("--audit-pending") {
            center.getPendingNotificationRequests { pending in
                let now = Date().timeIntervalSince1970
                let manifest = (try? Data(contentsOf: self.directory.appendingPathComponent("manifest.json"))).flatMap { try? JSONDecoder().decode(Manifest.self, from: $0) }
                let wanted = Dictionary((manifest?.notices ?? []).filter { $0.expires > now }.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
                let plan = repairPlan(wanted: wanted, pending: pending.map(pendingNotice), now: now, unavailable: [])
                print("expected=\(wanted.count) pending=\(pending.count) missing=0 changed=\(plan.changed) obsolete=\(plan.remove.count)")
                DispatchQueue.main.async { NSApp.terminate(nil) }
            }
            return
        }
        if CommandLine.arguments.contains("--status") {
            center.getNotificationSettings { settings in
                print("notification_authorization=\(settings.authorizationStatus.rawValue) alert_style=\(settings.alertStyle.rawValue) calendar_authorization=\(EKEventStore.authorizationStatus(for: .event).rawValue)")
                DispatchQueue.main.async { NSApp.terminate(nil) }
            }
            return
        }
        if CommandLine.arguments.contains("--test") {
            testNotification()
            DispatchQueue.main.asyncAfter(deadline: .now() + 3) { NSApp.terminate(nil) }
            return
        }
        guard let attributes = try? FileManager.default.attributesOfItem(atPath: directory.path),
              (attributes[.posixPermissions] as? NSNumber)?.intValue == 0o700,
              (attributes[.ownerAccountID] as? NSNumber)?.uint32Value == getuid(),
              attributes[.type] as? FileAttributeType == .typeDirectory else { NSApp.terminate(nil); return }
        lockFD = open(directory.appendingPathComponent("app.lock").path, O_CREAT | O_RDWR | O_NOFOLLOW, 0o600)
        guard lockFD >= 0, flock(lockFD, LOCK_EX | LOCK_NB) == 0 else { NSApp.terminate(nil); return }
        // launchctl bootout sends SIGTERM. Route it through AppKit cleanup so the
        // worker (lacia agent) and any configured port-forward stop with the app.
        Darwin.signal(SIGTERM, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
        source.setEventHandler { NSApp.terminate(nil) }
        source.resume()
        terminationSignal = source
        if let data = try? Data(contentsOf: ledgerURL), let saved = try? JSONDecoder().decode([String: Double].self, from: data) { ledger = saved }
        else if FileManager.default.fileExists(atPath: ledgerURL.path) { NSApp.terminate(nil); return }
        if let data = try? Data(contentsOf: meetingPlanURL), let saved = try? JSONDecoder().decode([Notice].self, from: data) { meetingPlan = saved }
        if let data = try? Data(contentsOf: cancellationURL), let saved = try? JSONDecoder().decode([String: Double].self, from: data) { cancelledMeetings = saved }
        if let data = try? Data(contentsOf: reconciliationURL), let saved = try? JSONDecoder().decode([String: Double].self, from: data) {
            lastReconciliation = saved["checked_at"] ?? 0
        }
        makeMenu()
        NotificationCenter.default.addObserver(forName: .EKEventStoreChanged, object: store, queue: .main) { [weak self] _ in self?.tick() }
        NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in self?.tick() }
        startWorker()
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in self?.tick() }
        tick()
        // Permission prompts are initiated by explicit menu actions after installation.
    }
    func config() -> Config? {
        guard let data = try? Data(contentsOf: configURL) else { return nil }
        return try? JSONDecoder().decode(Config.self, from: data)
    }
    func makeMenu() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let imageURL = Bundle.main.url(forResource: "brand", withExtension: "png"), let image = NSImage(contentsOf: imageURL) {
            image.size = NSSize(width: 20, height: 20); statusItem?.button?.image = image
        } else { statusItem?.button?.title = "L" }
        let menu = NSMenu()
        let status = NSMenuItem(title: "タスクの同期状況を確認しています", action: nil, keyEquivalent: "")
        menu.addItem(status)
        syncStatusItem = status
        let review = NSMenuItem(title: "退避した変更（要確認）", action: nil, keyEquivalent: "")
        review.isHidden = true
        menu.addItem(review)
        syncReviewItem = review
        menu.addItem(.separator())
        for (title, selector) in [("レイシアを開く", #selector(openHome)), ("通知を許可する", #selector(authorizeNotifications)), ("テストの通知を1件表示する", #selector(testNotification)), ("通知の設定を開く", #selector(openSettings)), ("このMacの設定ファイルを開く", #selector(openConfig)), ("終了する（常駐中は自動で起動し直します）", #selector(quit))] {
            let item = NSMenuItem(title: title, action: selector, keyEquivalent: ""); item.target = self; menu.addItem(item)
        }
        statusItem?.menu = menu
    }
    func updateSyncMenu(_ health: ManifestHealth?, fresh: Bool, repo: String?) {
        syncStatusItem?.title = syncLabel(health?.sync, fresh: fresh, configured: !(repo ?? "").isEmpty)
        guard fresh, health?.sync != nil else { return }
        let reviews = health?.sync_review ?? []
        guard reviews != syncReviews else { return }
        syncReviews = reviews
        syncReviewItem?.isHidden = reviews.isEmpty
        let submenu = NSMenu()
        for review in reviews {
            let ids = (review.tasks ?? []).filter { $0.range(of: "^[A-Z][0-9]{1,6}$", options: .regularExpression) != nil }
            let title = ids.isEmpty ? "タスクを特定できない変更" : "タスク \(ids.joined(separator: "、")) の変更"
            let item = NSMenuItem(title: title, action: #selector(openSyncBackup(_:)), keyEquivalent: "")
            item.target = self
            item.representedObject = review.path
            submenu.addItem(item)
        }
        syncReviewItem?.submenu = submenu
    }
    @objc func openSyncBackup(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String,
              let backup = syncBackupURL(path, repo: config()?.tasks_repo) else { return }
        NSWorkspace.shared.activateFileViewerSelecting([backup])
    }
    @objc func authorizeNotifications() {
        center.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in DispatchQueue.main.async { self.tick() } }
    }
    @objc func authorizeCalendar() {
        store.requestFullAccessToEvents { _, _ in DispatchQueue.main.async { self.tick() } }
    }
    @objc func openSettings() { NSWorkspace.shared.open(URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension")!) }
    @objc func openConfig() { NSWorkspace.shared.open(configURL) }
    @objc func openHome() { if let url = URL(string: siteBase(config()?.site) + "/") { NSWorkspace.shared.open(url) } }
    @objc func quit() { NSApp.terminate(nil) }
    @objc func testNotification() {
        let now = Date().timeIntervalSince1970
        let test = Notice(id: "test-" + UUID().uuidString, kind: "test", title: "テストの通知です", body: "通知を閉じてもタスクの状態は変わりません。", url: siteBase(config()?.site) + "/", fire: now, expires: now + 60)
        center.getNotificationSettings { settings in
            guard settings.authorizationStatus == .authorized else { return }
            let content = UNMutableNotificationContent()
            content.title = test.title; content.body = test.body; content.sound = .default
            content.userInfo = ["url": test.url]
            self.center.add(UNNotificationRequest(identifier: test.id, content: content, trigger: nil))

        }
    }
    func startWorker() {
        lastWorkerStart = Date().timeIntervalSince1970
        guard let cfg = config() else { return }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: cfg.lacia)
        process.arguments = ["agent", "--config", configURL.path, "--state", directory.path,
                             "--parent-pid", String(getpid())]
        process.standardOutput = FileHandle.nullDevice; process.standardError = FileHandle.nullDevice
        do { try process.run(); worker = process } catch { worker = nil }
    }
    func meetings(_ cfg: Config, _ now: Double) -> [Notice]? {
        guard EKEventStore.authorizationStatus(for: .event) == .fullAccess else { calendarHealth = "permission_required"; return nil }
        let calendars = store.calendars(for: .event)
        guard !calendars.isEmpty else { calendarHealth = "no_synced_calendars"; return nil }
        calendarHealth = "local_store_read_ok"
        let predicate = store.predicateForEvents(withStart: Date(timeIntervalSince1970: now), end: Date(timeIntervalSince1970: now + 86460), calendars: calendars)
        let events = store.events(matching: predicate)
        guard EKEventStore.authorizationStatus(for: .event) == .fullAccess else { calendarHealth = "permission_required"; return nil }
        var result: [String: Notice] = [:]
        for event in events {
            guard let start = event.startDate else { continue }
            let attendees = event.attendees ?? []
            guard meetingCandidate(allDay: event.isAllDay, canceled: event.status == .canceled,
                                   selfDeclined: attendees.contains { $0.isCurrentUser && $0.participantStatus == .declined },
                                   otherAttendee: attendees.contains { !$0.isCurrentUser }, url: event.url) else { continue }
            let externalID = event.calendarItemExternalIdentifier ?? event.calendarItemIdentifier
            let fields = ["account": event.calendar.source.sourceIdentifier, "calendar_id": event.calendar.calendarIdentifier,
                          "event_id": externalID, "title_contains": event.title ?? ""]
            if localExcluded(fields, cfg.exclude_meetings) { continue }
            let fire = start.timeIntervalSince1970 - 60
            guard now < start.timeIntervalSince1970, fire <= now + 86400 else { continue }
            // Same external event + occurrence across duplicate calendars is one meeting. Separate accounts remain distinct.
            let occurrence = event.occurrenceDate ?? start
            let id = "meeting-ek-" + digest([event.calendar.source.sourceIdentifier, externalID, String(occurrence.timeIntervalSince1970), String(start.timeIntervalSince1970)].joined(separator: "\u{0}"))
            let formatter = DateFormatter(); formatter.timeZone = TimeZone(identifier: "Asia/Tokyo"); formatter.dateFormat = "HH:mm"
            let url = event.url.flatMap { calendarEventAllows($0) ? $0.absoluteString : nil } ?? siteBase(cfg.site) + "/"
            result[id] = Notice(id: id, kind: "meeting", title: "会議が1分後に始まります", body: formatter.string(from: start) + " " + (event.title ?? "会議"), url: url, fire: fire, expires: start.timeIntervalSince1970)
        }
        return Array(result.values)
    }
    func readManifest(now: Double) -> (manifest: Manifest?, fresh: Bool) {
        let manifestURL = directory.appendingPathComponent("manifest.json")
        let manifest = (try? Data(contentsOf: manifestURL)).flatMap { try? JSONDecoder().decode(Manifest.self, from: $0) }
        let modified = (try? FileManager.default.attributesOfItem(atPath: manifestURL.path))?[.modificationDate] as? Date
        let written = manifest?.health?.checked_at ?? modified?.timeIntervalSince1970
        return (manifest, manifest?.version == 1 && (written.map { -2 <= now - $0 && now - $0 < 100 } ?? false))
    }
    func tick() {
        guard !busy else { return }; busy = true
        guard let cfg = config() else { busy = false; return }
        let now = Date().timeIntervalSince1970
        if worker?.isRunning != true && now - lastWorkerStart > 60 { startWorker() }
        let (manifest, manifestFresh) = readManifest(now: now)
        let manifestAvailable = canReconcileManifest(manifest, fresh: manifestFresh)
        updateSyncMenu(manifest?.health, fresh: manifestFresh, repo: cfg.tasks_repo)
        var notices = manifestNotices(manifest, config: cfg, fresh: manifestFresh)
        if cfg.meeting_source != "eventkit" {
            meetingPlan = meetingDeliveryPlan(manifest, config: cfg, fresh: manifestFresh, saved: meetingPlan, now: now)
            try? privateWrite(meetingPlan, meetingPlanURL)
            notices = notices.filter { $0.kind != "meeting" } + meetingPlan
        }
        var unavailablePrefixes: [String] = []
        var sourceAvailable = !cfg.enabled || !cfg.meeting_notifications
        if cfg.enabled {
            if let manifest, manifest.version == 1 {
                sourceAvailable = sourceAvailable || (manifest.health?.calendar == "ok" && manifest.health?.transport == "ok")
            }
            if !manifestAvailable {
                unavailablePrefixes.append("task-")
            }
            if cfg.meeting_notifications && cfg.meeting_source != "eventkit" && (!manifestAvailable || manifest?.health?.calendar == "unavailable") {
                unavailablePrefixes.append("meeting-")
            }
            if !manifestFresh || manifest?.health?.sync == nil { unavailablePrefixes.append("sync-") }
            if cfg.meeting_notifications && cfg.meeting_source == "eventkit" {
                if let currentMeetings = meetings(cfg, now) { notices += currentMeetings }
                else { unavailablePrefixes.append("meeting-ek-") }
            }
        }
        try? privateWrite(["checked_at": String(now), "calendar": calendarHealth, "worker": worker?.isRunning == true ? "running" : "stopped"], directory.appendingPathComponent("native-health.json"))
        let proposed = Dictionary(notices.filter { $0.expires > now }.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
        let planDigest = digest(String(data: (try? encoder.encode(notices.sorted { $0.id < $1.id })) ?? Data(), encoding: .utf8) ?? "")
        let periodic = reconciliationDue(now: now, checked: lastReconciliation)
        let audit = (periodic || planDigest != lastPlanDigest) && (now - lastAuditAttempt >= 30 || now < lastAuditAttempt)
        if audit { lastAuditAttempt = now }
        center.getNotificationSettings { settings in
            self.center.getPendingNotificationRequests { pending in
                DispatchQueue.main.async {
                    let pendingNotices = pending.map(pendingNotice)
                    let cancellations = meetingCancellations(saved: self.cancelledMeetings, accepted: self.ledger, pending: pendingNotices,
                                                             wanted: Set(proposed.keys), unavailable: unavailablePrefixes, now: now)
                    let cancellationsChanged = cancellations != self.cancelledMeetings
                    if cancellationsChanged, (try? privateWrite(cancellations, self.cancellationURL)) != nil {
                        self.cancelledMeetings = cancellations
                    }
                    let wanted = proposed.filter { cancellations[$0.key] == nil }
                    let plan = repairPlan(wanted: wanted, pending: pendingNotices, now: now, unavailable: unavailablePrefixes)
                    var cancelled: [String] = []
                    if cancellationsChanged || now - self.lastCancellationAttempt >= 30 || now < self.lastCancellationAttempt {
                        self.lastCancellationAttempt = now
                        if self.cancelledMeetings == cancellations {
                            for id in cancellations.keys where (self.ledger[id] ?? 0) > now { self.ledger.removeValue(forKey: id) }
                        }
                        cancelled = Array(cancellations.keys)
                    }
                    self.center.removePendingNotificationRequests(withIdentifiers: Array(Set(plan.remove + cancelled)))
                    if !cancelled.isEmpty {
                        self.center.removeDeliveredNotifications(withIdentifiers: cancelled)
                    }
                    for id in plan.remove where cancellations[id] == nil && (self.ledger[id] ?? 0) > now { self.ledger.removeValue(forKey: id) }
                    for id in plan.restore { self.ledger.removeValue(forKey: id) }
                    let group = DispatchGroup()
                    var failures = 0
                    var submitted = 0
                    if settings.authorizationStatus == .authorized {
                        let deliveryTime = Date().timeIntervalSince1970
                        var ready = wanted.values.filter { $0.kind != "meeting" }
                        if wanted.values.contains(where: { meetingIsDue($0, now: deliveryTime) && self.ledger[$0.id] == nil }) {
                            if cfg.meeting_source == "eventkit" {
                                ready += (self.meetings(cfg, deliveryTime) ?? []).filter { meetingIsDue($0, now: deliveryTime) }
                            } else {
                                let (latest, fresh) = self.readManifest(now: deliveryTime)
                                ready += dueManifestMeetings(latest, config: cfg, fresh: fresh, now: deliveryTime, saved: self.meetingPlan)
                            }
                        }
                        for notice in ready where self.ledger[notice.id] == nil && cancellations[notice.id] == nil {
                            group.enter(); submitted += 1
                            self.add(notice, deliveryTime) { success in
                                if !success { failures += 1 }
                                group.leave()
                              }
                        }
                    }
                    group.notify(queue: .main) {
                        self.center.getPendingNotificationRequests { verified in
                            DispatchQueue.main.async {
                                let verificationNow = Date().timeIntervalSince1970
                                let remaining = repairPlan(wanted: wanted, pending: verified.map(pendingNotice), now: verificationNow, unavailable: unavailablePrefixes)
                                self.ledger = self.ledger.filter { $0.value > now - 30 * 86400 }
                                try? privateWrite(self.ledger, self.ledgerURL)
                                if audit {
                                    let success = (manifestAvailable || !cfg.enabled) && settings.authorizationStatus == .authorized && failures == 0 && remaining.restore.isEmpty && remaining.remove.isEmpty
                                    let result: [String: Double] = ["checked_at": success ? now : self.lastReconciliation, "attempted_at": now, "expected": Double(wanted.count), "pending": Double(pending.count), "missing": 0, "changed": Double(plan.changed), "removed": Double(plan.remove.count), "submitted": Double(submitted), "failures": Double(failures), "remaining_missing": 0, "remaining_changed": Double(remaining.changed), "remaining_obsolete": Double(remaining.remove.count), "source_unavailable": manifestFresh && sourceAvailable ? 0 : 1, "authorized": settings.authorizationStatus == .authorized ? 1 : 0]
                                    try? privateWrite(result, self.reconciliationURL)
                                    let logURL = self.directory.appendingPathComponent("reconciliation.log")
                                    if let size = (try? FileManager.default.attributesOfItem(atPath: logURL.path))?[.size] as? NSNumber, size.intValue > 2 * 1024 * 1024 { try? FileManager.default.removeItem(at: logURL) }
                                    if !FileManager.default.fileExists(atPath: logURL.path) { FileManager.default.createFile(atPath: logURL.path, contents: nil, attributes: [.posixPermissions: 0o600]) }
                                    if let handle = try? FileHandle(forWritingTo: logURL) {
                                        _ = try? handle.seekToEnd()
                                        if let data = try? JSONEncoder().encode(result) { try? handle.write(contentsOf: data + Data([10])) }
                                        try? handle.close()
                                    }
                                    if success {
                                        self.lastReconciliation = now
                                        self.lastPlanDigest = planDigest
                                    }
                                }
                                self.busy = false
                            }
                        }
                    }
                }
            }
        }
    }
    func add(_ notice: Notice, _ now: Double, completion: @escaping (Bool) -> Void) {
        guard self.ledger[notice.id] == nil, notice.expires > now else { completion(false); return }
        let content = UNMutableNotificationContent()
        content.title = notice.kind == "meeting" && now - notice.fire >= 10 ? "会議がまもなく始まります" : notice.title
        content.body = notice.body; content.sound = .default
        content.userInfo = ["url": notice.url]
        let delay = notice.fire - now
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let components = calendar.dateComponents([.calendar, .timeZone, .year, .month, .day, .hour, .minute, .second], from: Date(timeIntervalSince1970: notice.fire))
        let trigger: UNNotificationTrigger? = notice.kind != "meeting" && delay > 1 ? UNCalendarNotificationTrigger(dateMatching: components, repeats: false) : nil
        self.ledger[notice.id] = notice.fire
        do { try privateWrite(self.ledger, self.ledgerURL) } catch { self.ledger.removeValue(forKey: notice.id); completion(false); return }
        center.add(UNNotificationRequest(identifier: notice.id, content: content, trigger: trigger)) { error in
            DispatchQueue.main.async {
                if error != nil { self.ledger.removeValue(forKey: notice.id); try? privateWrite(self.ledger, self.ledgerURL) }
                completion(error == nil)
            }
        }
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        DispatchQueue.main.async {
            let cancelled = self.cancelledMeetings[notification.request.identifier] != nil
            completionHandler(cancelled ? [] : [.banner, .list, .sound])
        }
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier,
           let value = response.notification.request.content.userInfo["url"] as? String,
           let url = notificationDestination(value, id: response.notification.request.identifier, site: config()?.site) {
            NSWorkspace.shared.open(url)
        }
        completionHandler()
    }
    func applicationWillTerminate(_ notification: Notification) {
        timer?.invalidate(); worker?.terminate()
        if lockFD >= 0 { close(lockFD) }
    }
}
if CommandLine.arguments.contains("--self-test") {
    precondition(siteAllows(URL(string: defaultSite + "/tasks/T1001")!, site: nil))
    precondition(!siteAllows(URL(string: "https://example.com/")!, site: nil))
    precondition(!siteAllows(URL(string: defaultSite + "/?a=1")!, site: ""))
    precondition(siteAllows(URL(string: "http://127.0.0.1:10148/tasks/T1001")!, site: "http://127.0.0.1:10148/"))
    precondition(!siteAllows(URL(string: "http://127.0.0.1:9999/")!, site: "http://127.0.0.1:10148"))
    precondition(!siteAllows(URL(string: "http://example.com/")!, site: "http://example.com"))
    precondition(siteAllows(URL(string: "https://lacia.example/tasks/T1001")!, site: "https://lacia.example"))
    precondition(!siteAllows(URL(string: "https://lacia.example/")!, site: "http://127.0.0.1:10148"))
    precondition(!siteAllows(URL(string: "https://lacia.example/")!, site: nil))
    precondition(!siteAllows(URL(string: "http://lacia.example/")!, site: "https://lacia.example"))
    let eventURL = "https://calendar.google.com/calendar/event?eid=instance&ctz=Asia%2FTokyo"
    precondition(notificationDestination(eventURL, id: "meeting-fixture", site: nil)?.absoluteString == eventURL)
    precondition(notificationDestination("https://www.google.com/calendar/event?eid=instance", id: "meeting-ek-fixture", site: nil) != nil)
    precondition(notificationDestination(eventURL, id: "task-fixture", site: nil) == nil)
    precondition(notificationDestination(defaultSite + "/", id: "meeting-fixture", site: nil) != nil)
    precondition(notificationDestination(defaultSite + "/tasks/T1001", id: "task-fixture", site: nil) != nil)
    for value in [
        "https://meet.google.com/fixture",
        "http://calendar.google.com/calendar/event?eid=instance",
        "https://calendar.google.com.example.test/calendar/event?eid=instance",
        "https://user@calendar.google.com/calendar/event?eid=instance",
        "https://calendar.google.com:8443/calendar/event?eid=instance",
        "https://calendar.google.com/%63alendar/event?eid=instance",
        "https://calendar.google.com/calendar/event?eid=",
        "https://calendar.google.com/calendar/event?ctz=Asia%2FTokyo",
        "https://calendar.google.com/calendar/event?eid=instance&unexpected=value",
        "https://calendar.google.com/calendar/event?eid=instance#fragment"
    ] {
        precondition(notificationDestination(value, id: "meeting-fixture", site: nil) == nil)
    }
    func candidate(_ day: Bool = false, _ canceled: Bool = false, _ declined: Bool = false, _ attendee: Bool = true, _ link: String? = nil) -> Bool {
        meetingCandidate(allDay: day, canceled: canceled, selfDeclined: declined, otherAttendee: attendee, url: link.flatMap(URL.init(string:)))
    }
    precondition(!removePending(id: "meeting-ek-fixture", wanted: [], unavailablePrefixes: ["meeting-ek-"]))
    precondition(removePending(id: "meeting-ek-fixture", wanted: [], unavailablePrefixes: []))
    precondition(!removePending(id: "meeting-ek-fixture", wanted: ["meeting-ek-fixture"], unavailablePrefixes: []))
    precondition(removePending(id: "task-fixture", wanted: [], unavailablePrefixes: ["meeting-ek-"]))
    let sync = Notice(id: "sync-fixture", kind: "sync", title: "fixture", body: "body", url: "", fire: 100, expires: 200)
    let syncManifest = Manifest(version: 1, notices: [sync], health: nil)
    var taskOnly = Config(enabled: true, meeting_notifications: false, meeting_source: "eventkit", exclude_meetings: [], lacia: "")
    precondition(manifestNotices(syncManifest, config: taskOnly, fresh: true).map(\.id) == [sync.id])
    taskOnly.meeting_notifications = true
    taskOnly.meeting_source = "linked"
    precondition(manifestNotices(syncManifest, config: taskOnly, fresh: false).isEmpty)
    precondition(!removePending(id: sync.id, wanted: [], unavailablePrefixes: ["sync-"]))
    precondition(removePending(id: sync.id, wanted: [], unavailablePrefixes: []))
    taskOnly.enabled = false
    precondition(manifestNotices(syncManifest, config: taskOnly, fresh: true).isEmpty)
    let oldManifest = Data("""
        {"version":1,"notices":[],"health":{"calendar":"ok","transport":"ok"}}
        """.utf8)
    precondition((try! JSONDecoder().decode(Manifest.self, from: oldManifest)).health?.sync == nil)
    let fileManager = FileManager.default
    let testRoot = fileManager.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
    let repo = testRoot.appendingPathComponent("mirror", isDirectory: true)
    let local = repo.appendingPathComponent("local", isDirectory: true)
    let backup = local.appendingPathComponent("backup", isDirectory: true)
    let other = testRoot.appendingPathComponent("outside", isDirectory: true)
    let link = local.appendingPathComponent("link")
    try! fileManager.createDirectory(at: backup, withIntermediateDirectories: true)
    try! fileManager.createDirectory(at: other, withIntermediateDirectories: true)
    try! fileManager.createSymbolicLink(at: link, withDestinationURL: other)
    precondition(syncBackupURL(backup.path, repo: repo.path)?.path == backup.resolvingSymlinksInPath().path)
    precondition(syncBackupURL(local.path, repo: repo.path) == nil)
    precondition(syncBackupURL(other.path, repo: repo.path) == nil)
    precondition(syncBackupURL(link.path, repo: repo.path) == nil)
    precondition(syncBackupURL(backup.path + "/../backup", repo: repo.path) == nil)
    try! fileManager.removeItem(at: testRoot)
    precondition(candidate())
    precondition(!candidate(true) && !candidate(false, true) && !candidate(false, false, true))
    precondition(!candidate(false, false, false, false))
    precondition(candidate(false, false, false, false, "https://meet.google.com/fixture"))
    precondition(!candidate(false, false, false, false, "https://meet.google.com.example.test/fixture"))
    precondition(localExcluded(["title_contains": "Weekly [fixture]"], [["title_contains": "[fixture]"]]))
    precondition(!localExcluded(["title_contains": "Weekly fixture"], [["title_contains": ".*"]]))
    precondition(!localExcluded(["calendar_id": "a"], [["calendar_id": "b"]]))
    precondition(digest("a\u{0}b") != digest("ab"))
    let future = Notice(id: "meeting-fixture", kind: "meeting", title: "fixture", body: "body", url: "https://example.test/", fire: 1000, expires: 1060)
    let expected = [future.id: future]
    let registered = PendingNotice(id: future.id, title: future.title, body: future.body, url: future.url, fire: future.fire)
    precondition(repairPlan(wanted: expected, pending: [], now: 100, unavailable: []).restore.isEmpty)
    precondition(repairPlan(wanted: expected, pending: [registered, registered], now: 100, unavailable: []).restore.isEmpty)
    var shifted = registered; shifted.fire = 1100
    precondition(repairPlan(wanted: expected, pending: [shifted], now: 100, unavailable: []).changed == 1)
    var previousURL = registered; previousURL.url = defaultSite + "/"
    let urlRepair = repairPlan(wanted: expected, pending: [previousURL], now: 100, unavailable: [])
    precondition(urlRepair.remove == [future.id] && urlRepair.restore == [future.id])
    precondition(repairPlan(wanted: [:], pending: [registered], now: 100, unavailable: ["meeting-"]).remove.isEmpty)
    precondition(repairPlan(wanted: [:], pending: [registered], now: 100, unavailable: []).remove == [future.id])
    let linked = Config(enabled: true, meeting_notifications: true, meeting_source: "linked", exclude_meetings: [], lacia: "")
    let partial = ManifestHealth(calendar: "partial", transport: "ok", sync: nil, sync_review: nil)
    let verified = Manifest(version: 1, notices: [future], health: partial, confirmed_meetings: [future.id], meeting_checks: [future.id: 1])
    let held = Manifest(version: 1, notices: [future], health: partial, confirmed_meetings: [], meeting_checks: [future.id: 1])
    let absent = Manifest(version: 1, notices: [], health: partial, confirmed_meetings: [])
    precondition(dueManifestMeetings(verified, config: linked, fresh: true, now: 999).isEmpty)
    precondition(dueManifestMeetings(verified, config: linked, fresh: true, now: 1000).map(\.id) == [future.id])
    precondition(dueManifestMeetings(held, config: linked, fresh: true, now: 1000).map(\.id) == [future.id])
    precondition(dueManifestMeetings(absent, config: linked, fresh: true, now: 1000).isEmpty)
    precondition(dueManifestMeetings(verified, config: linked, fresh: false, now: 1000, saved: [future]).map(\.id) == [future.id])
    precondition(dueManifestMeetings(nil, config: linked, fresh: false, now: 1000, saved: [future]).map(\.id) == [future.id])
    precondition(dueManifestMeetings(absent, config: linked, fresh: true, now: 1000, saved: [future]).isEmpty)
    precondition(dueManifestMeetings(verified, config: linked, fresh: true, now: 1060).isEmpty)
    let awaiting = Manifest(version: 1, notices: [future], health: partial, confirmed_meetings: [])
    precondition(dueManifestMeetings(awaiting, config: linked, fresh: true, now: 1000, saved: [future]).isEmpty)
    precondition(dueManifestMeetings(awaiting, config: linked, fresh: true, now: 1035, saved: [future]).map(\.id) == [future.id])
    precondition(dueManifestMeetings(nil, config: linked, fresh: false, now: 1060, saved: [future]).isEmpty)
    let moved = Notice(id: "meeting-moved", kind: "meeting", title: "moved", body: "body", url: "", fire: 2000, expires: 2060)
    let movedManifest = Manifest(version: 1, notices: [moved], health: partial)
    precondition(meetingDeliveryPlan(movedManifest, config: linked, fresh: true, saved: [future], now: 1000).map(\.id) == [moved.id])
    let accepted = [future.id: future.fire, "meeting-elapsed": 99, "task-fixture": 1000]
    let offline = Manifest(version: 1, notices: [], health: ManifestHealth(calendar: "disabled", transport: "failed", sync: nil, sync_review: nil))
    precondition(!canReconcileManifest(offline, fresh: true))
    precondition(!canReconcileManifest(nil, fresh: false))
    let cancelled = meetingCancellations(saved: [:], accepted: accepted, pending: [], wanted: [], unavailable: [], now: 100)
    precondition(cancelled == [future.id: future.fire])
    precondition(meetingCancellations(saved: [:], accepted: [:], pending: [registered], wanted: [], unavailable: [], now: 100) == cancelled)
    precondition(meetingCancellations(saved: [:], accepted: accepted, pending: [], wanted: [], unavailable: ["meeting-"], now: 100).isEmpty)
    precondition(meetingCancellations(saved: cancelled, accepted: [:], pending: [], wanted: [future.id], unavailable: ["meeting-"], now: 1100) == cancelled)
    precondition(meetingCancellations(saved: cancelled, accepted: [:], pending: [], wanted: [future.id], unavailable: [], now: 1100).isEmpty)
    precondition(meetingCancellations(saved: cancelled, accepted: [:], pending: [], wanted: [], unavailable: [], now: 87400).isEmpty)
    precondition(reconciliationDue(now: 10801, checked: 1))
    precondition(!reconciliationDue(now: 10800, checked: 1))
    precondition(reconciliationDue(now: 100, checked: 0) && reconciliationDue(now: 100, checked: 101))
    print("native_policy_fixtures_passed")
    exit(0)
}
let application = NSApplication.shared
let delegate = App()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
