// Dark mode toggle + copy-to-clipboard helpers
(function(){
  const root = document.documentElement;
  const toggle = document.getElementById('themeToggle');
  const key = 'gomap.theme';
  const saved = localStorage.getItem(key);
  if(saved === 'light') root.classList.add('light');
  if(saved === 'dark') root.classList.remove('light');
  function updateIcon(){ toggle.textContent = root.classList.contains('light') ? '🌙' : '🌞'; }
  toggle?.addEventListener('click', () => {
    root.classList.toggle('light');
    localStorage.setItem(key, root.classList.contains('light') ? 'light' : 'dark');
    updateIcon();
  });
  updateIcon();

  // Copy buttons
  document.querySelectorAll('.code').forEach(block => {
    const btn = block.querySelector('.copy');
    const code = block.querySelector('code');
    btn?.addEventListener('click', async () => {
      try{
        await navigator.clipboard.writeText(code.textContent);
        const old = btn.textContent; btn.textContent = 'Copied!';
        setTimeout(()=> btn.textContent = old, 1200);
      }catch(e){ console.error('Copy failed', e); }
    });
  });

  // Year in footer
  const year = document.getElementById('year');
  if(year) year.textContent = new Date().getFullYear();
})();
