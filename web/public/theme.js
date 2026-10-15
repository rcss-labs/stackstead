(function () {
  var h = document.documentElement, q = new URLSearchParams(location.search).get('theme');
  try {
    var t = localStorage.getItem('stackstead.theme');
    if (t === 'dark' || t === 'light') h.dataset.theme = t;
    if (localStorage.getItem('stackstead.density') === 'comfortable') h.dataset.density = 'comfortable';
  } catch (e) {}
  if (q === 'dark' || q === 'light') h.dataset.theme = q; // one-off, not persisted; used by the e2e render
})();
