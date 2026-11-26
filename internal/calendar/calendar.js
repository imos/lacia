// Keeps the current-time line of the calendar on the real JST minute.
// It only touches the three clock elements and never fetches anything.
(function () {
  'use strict';
  var AXIS_START = 480, AXIS_END = 1200, JST_OFFSET = 9 * 3600000;

  // Returns what to show for the day "YYYY-MM-DD" at the instant now (ms).
  function calendarNowMarker(day, now) {
    if (!Number.isFinite(now)) return {visible: false};
    var jst = new Date(now + JST_OFFSET).toISOString();
    if (jst.slice(0, 10) !== day) return {visible: false};
    var clock = jst.slice(11, 16);
    var minute = Number(clock.slice(0, 2)) * 60 + Number(clock.slice(3));
    var inside = minute >= AXIS_START && minute <= AXIS_END;
    return {
      visible: true,
      inside: inside,
      label: '現在 ' + clock + (minute < AXIS_START ? ' · 08:00より前' : minute > AXIS_END ? ' · 20:00より後' : ''),
      position: inside ? (minute - AXIS_START) / (AXIS_END - AXIS_START) * 100 : null
    };
  }

  function update() {
    var roots = document.querySelectorAll('[data-calendar-day]');
    for (var i = 0; i < roots.length; i++) {
      var root = roots[i];
      var badge = root.querySelector('.calendar-now-status');
      var line = root.querySelector('.calendar-now-line');
      var band = root.querySelector('.calendar-band-now');
      if (!badge || !line || !band) continue;
      var mark = calendarNowMarker(root.getAttribute('data-calendar-day'), Date.now());
      badge.hidden = !mark.visible;
      line.hidden = band.hidden = !mark.visible || !mark.inside;
      if (mark.visible && badge.textContent !== mark.label) badge.textContent = mark.label;
      if (mark.visible && mark.inside) {
        line.style.top = mark.position + '%';
        band.style.left = mark.position + '%';
      }
    }
  }

  if (typeof module !== 'undefined') module.exports = {calendarNowMarker: calendarNowMarker};
  if (typeof document === 'undefined') return;
  update();
  setInterval(update, 60000);
  window.addEventListener('focus', update);
  document.addEventListener('visibilitychange', update);
})();
