# Карандаш-дизайн — обводка, покачивание, живость

## Карточка idle
```css
.widget-card {
  background: #FFFBF0;
  background-image: radial-gradient(rgba(34,34,34,0.08) 1px, transparent 1px);
  background-size: 16px 16px;
  border: 2px dashed #222;
  border-radius: 14px 18px 16px 12px; /* hand-drawn неровный */
  box-shadow: 2px 3px 0 #111;
  padding: 20px;
  position: relative;
}
.widget-card::before {
  content: "";
  position: absolute; inset: 0;
  border-radius: inherit;
  background: var(--pencil-color, #FFD93D);
  opacity: 0.14;
  background-image: repeating-linear-gradient(45deg, transparent 0 6px, rgba(0,0,0,0.04) 6px 7px);
  pointer-events: none;
}
```

## Hover — чёрный карандаш + покачивание + живость
```css
.widget-card:hover {
  border: 2.5px solid #111;
  border-radius: 16px 14px 18px 16px;
  box-shadow: 3px 4px 0 #111;
  filter: url(#pencil-rough); /* SVG ниже */
  animation: wobble 0.6s ease-in-out infinite, pencil-live 2.5s ease-in-out infinite;
  transform-origin: center;
}

@keyframes wobble {
  0%,100% { transform: rotate(-0.4deg) translate(0.5px,0.3px); }
  50%     { transform: rotate(0.4deg) translate(-0.5px,-0.2px); }
}

@keyframes pencil-live {
  0%,100% { stroke-dashoffset: 0; }
  50%     { stroke-dashoffset: 12; }
}

/* живость — SVG фильтр */
```
```svg
<svg width="0" height="0">
  <filter id="pencil-rough">
    <feTurbulence baseFrequency="0.018" numOctaves="2" seed="2" result="noise"/>
    <feDisplacementMap in="SourceGraphic" in2="noise" scale="1.2" xChannelSelector="R" yChannelSelector="G"/>
  </filter>
</svg>
```
```js
// лёгкая живость — меняем baseFrequency каждые 600ms
let f = 0.015;
setInterval(() => {
  f = 0.015 + Math.random()*0.007;
  document.querySelector('#pencil-rough feTurbulence').setAttribute('baseFrequency', f);
}, 600);
```

## Токены
- Цвета заливки: `--pencil-color` из `tokens.json` (red/yellow/mint/blue/pink)
- Transition: `all 180ms ease-out`
- Курсор: `grab` idle, `grabbing` drag
- Jiggle (edit mode): тот же `wobble` но `0.8s` + `scale(1.02)`

## Примечания дизайнеру
- Обводка не статична — за счёт `feTurbulence` + `dashoffset` выглядит будто рукой.
- Покачивание среднее — не тряска, `±0.4deg` достаточно.
- На мобиле hover → `active` (tap).
