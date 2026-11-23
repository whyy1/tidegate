// Applies a persisted color-scheme override before first paint (loaded synchronously from <head>).
// "light" / "dark" pin the scheme; anything else keeps "light dark" so the page follows the OS.
try {
  var stored = window.localStorage.getItem('color-scheme')
  var val = stored === 'light' || stored === 'dark' ? stored : 'light dark'
  var meta = document.querySelector('meta[name="color-scheme"]')
  if (meta) meta.setAttribute('content', val)
  document.documentElement.style.colorScheme = val
} catch (e) {}
