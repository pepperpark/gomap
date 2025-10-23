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
    if(!btn || !code) return;
    btn.addEventListener('click', async () => {
      const text = code.textContent ?? '';
      try{
        if(navigator.clipboard?.writeText){
          await navigator.clipboard.writeText(text);
        }else{
          // Fallback for non-secure contexts and older browsers
          const textarea = document.createElement('textarea');
          textarea.value = text;
          textarea.style.position = 'fixed';
          textarea.style.opacity = '0';
          document.body.appendChild(textarea);
          textarea.focus();
          textarea.select();
          const ok = document.execCommand('copy');
          document.body.removeChild(textarea);
          if(!ok) throw new Error('execCommand copy failed');
        }
        const old = btn.textContent; btn.textContent = 'Copied!';
        setTimeout(()=> btn.textContent = old, 1200);
      }catch(e){ console.error('Copy failed', e); }
    });
  });

  // Year in footer
  const year = document.getElementById('year');
  if(year) year.textContent = new Date().getFullYear();

  // Generate PNG favicons from SVG for broader compatibility
  async function generatePngIcon(size, rel, extraAttrs = {}){
    try{
      const svgText = await (await fetch('./icon.svg')).text();
      const svgBlob = new Blob([svgText], { type: 'image/svg+xml' });
      const url = URL.createObjectURL(svgBlob);
      await new Promise((resolve, reject)=>{
        const img = new Image();
        // Ensure proper rendering in Firefox for cross-origin
        img.crossOrigin = 'anonymous';
        img.onload = () => {
          const canvas = document.createElement('canvas');
          canvas.width = size; canvas.height = size;
          const ctx = canvas.getContext('2d');
          ctx.clearRect(0,0,size,size);
          ctx.drawImage(img, 0, 0, size, size);
          const dataUrl = canvas.toDataURL('image/png');
          const link = document.createElement('link');
          link.setAttribute('rel', rel);
          link.setAttribute('type', 'image/png');
          link.setAttribute('sizes', `${size}x${size}`);
          Object.entries(extraAttrs).forEach(([k,v])=>link.setAttribute(k,v));
          link.href = dataUrl;
          document.head.appendChild(link);
          URL.revokeObjectURL(url);
          resolve();
        };
        img.onerror = reject;
        img.src = url;
      });
    }catch(e){
      // Non-fatal; browsers will fallback to SVG favicon
      console.warn('PNG favicon generation failed', e);
    }
  }

  // Inject 32x32 favicon and 180x180 apple touch icon
  generatePngIcon(32, 'icon');
  generatePngIcon(180, 'apple-touch-icon');
})();
