package evidence

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MarshalHTML returns a self-contained, offline review page for a replay
// bundle. The page makes no network requests: telemetry and evidence are
// embedded in the file so the exact artifact a moderator reviewed can be
// archived with the case. encoding/json's HTML escaping prevents values such
// as player names from terminating the script element.
func MarshalHTML(bundle *ReplayBundle) ([]byte, error) {
	if bundle == nil {
		return nil, fmt.Errorf("evidence: nil replay bundle")
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshalling replay bundle for HTML: %w", err)
	}
	return []byte(strings.Replace(reviewHTML, "__NEVR_BUNDLE__", string(data), 1)), nil
}

const reviewHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'">
<title>NEVR evidence review</title>
<style>
:root{color-scheme:dark;--bg:#0b0f17;--panel:#131a26;--line:#293449;--text:#e7edf7;--muted:#99a7bd;--blue:#35a7ff;--orange:#ff8a34;--red:#ff5364;--green:#4bd69f}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 system-ui,-apple-system,Segoe UI,sans-serif}
header{padding:18px 22px;border-bottom:1px solid var(--line);display:flex;gap:20px;align-items:end;justify-content:space-between;flex-wrap:wrap}
h1,h2,p{margin:0}h1{font-size:20px}.muted{color:var(--muted)}.pill{display:inline-block;padding:3px 8px;border:1px solid var(--line);border-radius:999px;margin:3px 4px 0 0}
main{display:grid;grid-template-columns:minmax(520px,1fr) 380px;gap:14px;padding:14px;min-height:calc(100vh - 85px)}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;overflow:hidden}.stage{padding:14px;display:flex;flex-direction:column;gap:12px}
canvas{display:block;width:100%;height:min(62vh,720px);background:#070a10;border:1px solid var(--line);border-radius:8px}
.controls{display:grid;grid-template-columns:auto auto 1fr auto;align-items:center;gap:10px}button{background:#202b3e;color:var(--text);border:1px solid #34435d;border-radius:7px;padding:7px 10px;cursor:pointer}button:hover,button:focus{border-color:#6b82a6}input[type=range]{width:100%}
aside{display:flex;flex-direction:column;min-height:0}.aside-head{padding:14px;border-bottom:1px solid var(--line)}#events{padding:8px;overflow:auto;max-height:45vh}.event{width:100%;text-align:left;margin-bottom:7px;display:grid;grid-template-columns:1fr auto;gap:4px}.event.active{border-color:var(--red);background:#341d29}.event small{color:var(--muted)}
#details{padding:14px;border-top:1px solid var(--line);overflow:auto;white-space:pre-wrap;word-break:break-word;flex:1}.legend{display:flex;gap:14px;flex-wrap:wrap}.dot{width:9px;height:9px;border-radius:50%;display:inline-block;margin-right:5px}.warning{color:#ffcc7a}
@media(max-width:950px){main{grid-template-columns:1fr}aside{min-height:520px}canvas{height:55vh}}
</style>
</head>
<body>
<header><div><h1>NEVR evidence review</h1><p id="identity" class="muted"></p></div><div id="meta"></div></header>
<main>
  <section class="panel stage">
    <canvas id="arena" width="1200" height="760" aria-label="Top-down replay view"></canvas>
    <div class="controls"><button id="play" type="button">Play</button><button id="prev" type="button">−1</button><input id="timeline" type="range" min="0" max="0" value="0" aria-label="Replay frame"><button id="next" type="button">+1</button></div>
    <div><strong id="frameLabel"></strong> <span id="state" class="muted"></span></div>
    <div class="legend"><span><i class="dot" style="background:var(--blue)"></i>Blue</span><span><i class="dot" style="background:var(--orange)"></i>Orange</span><span><i class="dot" style="background:var(--green)"></i>Selected player</span><span><i class="dot" style="background:white"></i>Disc</span></div>
    <p class="muted">Top-down X/Z view. Lines connect tracked hands to each player. Red rings mark stunned players; a gold ring marks possession.</p>
  </section>
  <aside class="panel"><div class="aside-head"><h2>Detection events</h2><p class="muted">Select an event to jump to its evidence frame.</p></div><div id="events"></div><pre id="details"></pre></aside>
</main>
<script>
'use strict';
const bundle=__NEVR_BUNDLE__;
const framesByIndex=new Map();
for(const f of bundle.frames||[]){if(!framesByIndex.has(f.frame_index))framesByIndex.set(f.frame_index,[]);framesByIndex.get(f.frame_index).push(f)}
const indices=[...framesByIndex.keys()].sort((a,b)=>a-b);
const events=(bundle.detection_events||[]).slice().sort((a,b)=>a.frame_index-b.frame_index||a.detector_id.localeCompare(b.detector_id));
const canvas=document.getElementById('arena'),ctx=canvas.getContext('2d'),slider=document.getElementById('timeline');
slider.max=Math.max(0,indices.length-1);
let cursor=0,timer=null,selectedEvent=-1;
const names=(bundle.match_context&&bundle.match_context.player_names)||{};
const displayName=id=>names[id]||id;
document.getElementById('identity').textContent='Match '+bundle.match_id+' · Player '+displayName(bundle.player_id)+(bundle.case_id?' · Case '+bundle.case_id:'');
const meta=document.getElementById('meta');
for(const [k,v] of Object.entries(bundle.metadata||{})){const s=document.createElement('span');s.className='pill';s.textContent=k.replaceAll('_',' ')+': '+v;meta.appendChild(s)}
const eventList=document.getElementById('events');
events.forEach((ev,i)=>{const b=document.createElement('button');b.className='event';b.innerHTML='<strong></strong><small></small><span></span><small></small>';b.children[0].textContent=ev.detector_id+(ev.is_shadow?' · shadow':'');b.children[1].textContent='frame '+ev.frame_index;b.children[2].textContent=ev.observed_value||'No observed value';b.children[3].textContent='severity '+(100*ev.severity).toFixed(0)+'% · confidence '+(100*ev.confidence).toFixed(0)+'%';b.onclick=()=>selectEvent(i);eventList.appendChild(b)});
function closestFrame(target){let best=0,dist=Infinity;indices.forEach((v,i)=>{const d=Math.abs(v-target);if(d<dist){dist=d;best=i}});return best}
function selectEvent(i){selectedEvent=i;cursor=closestFrame(events[i].frame_index);slider.value=cursor;[...eventList.children].forEach((n,j)=>n.classList.toggle('active',j===i));document.getElementById('details').textContent=JSON.stringify(events[i],null,2);draw()}
function bounds(){const p=bundle.match_context&&bundle.match_context.physics||{};let width=p.arena_width||32,length=p.arena_length||80;if(!Number.isFinite(width)||width<=0)width=32;if(!Number.isFinite(length)||length<=0)length=80;return{width,length,goalZ:p.goal_z||36.078,goalRadius:p.goal_radius||1}}
function draw(){ctx.clearRect(0,0,canvas.width,canvas.height);const b=bounds(),pad=42,sx=(canvas.width-pad*2)/b.width,sz=(canvas.height-pad*2)/b.length,scale=Math.min(sx,sz),ox=canvas.width/2,oz=canvas.height/2;const point=v=>[ox+v[0]*scale,oz+v[2]*scale];
  ctx.strokeStyle='#293449';ctx.lineWidth=2;ctx.strokeRect(ox-b.width*scale/2,oz-b.length*scale/2,b.width*scale,b.length*scale);ctx.setLineDash([8,8]);ctx.beginPath();ctx.moveTo(ox-b.width*scale/2,oz);ctx.lineTo(ox+b.width*scale/2,oz);ctx.stroke();ctx.setLineDash([]);
  ctx.strokeStyle='#68758a';for(const z of [-b.goalZ,b.goalZ]){ctx.beginPath();ctx.arc(...point([0,0,z]),Math.max(5,b.goalRadius*scale),0,Math.PI*2);ctx.stroke()}
  if(!indices.length){ctx.fillStyle='#99a7bd';ctx.font='22px system-ui';ctx.fillText('No telemetry frames in this bundle',40,60);return}
  const idx=indices[cursor],frames=framesByIndex.get(idx)||[];let disc=null;
  for(const f of frames){const pos=point(f.position),selected=f.player_id===bundle.player_id,color=selected?'#4bd69f':(String(f.team).toLowerCase()==='orange'?'#ff8a34':'#35a7ff');ctx.strokeStyle=color;ctx.lineWidth=selected?4:2;
    for(const hand of [f.left_hand_position,f.right_hand_position]){if(hand&&hand.some(n=>n!==0)){const hp=point(hand);ctx.beginPath();ctx.moveTo(...pos);ctx.lineTo(...hp);ctx.stroke();ctx.fillStyle=color;ctx.beginPath();ctx.arc(...hp,4,0,Math.PI*2);ctx.fill()}}
    ctx.fillStyle=color;ctx.beginPath();ctx.arc(...pos,selected?9:7,0,Math.PI*2);ctx.fill();if(f.is_stunned){ctx.strokeStyle='#ff5364';ctx.lineWidth=3;ctx.beginPath();ctx.arc(...pos,14,0,Math.PI*2);ctx.stroke()}if(f.has_possession){ctx.strokeStyle='#ffd166';ctx.lineWidth=3;ctx.beginPath();ctx.arc(...pos,18,0,Math.PI*2);ctx.stroke()}if(f.disc)disc=f.disc;
    ctx.fillStyle='#d6deeb';ctx.font='12px system-ui';ctx.fillText(displayName(f.player_id),pos[0]+11,pos[1]-9)}
  if(disc&&disc.position){const dp=point(disc.position);ctx.fillStyle='#fff';ctx.beginPath();ctx.arc(...dp,6,0,Math.PI*2);ctx.fill();ctx.strokeStyle='#101826';ctx.stroke()}
  const active=events.filter(e=>(e.frame_range_start<=idx&&e.frame_range_end>=idx)||e.frame_index===idx);document.getElementById('frameLabel').textContent='Frame '+idx;const t=frames[0]?frames[0].timestamp:0;document.getElementById('state').textContent='t='+Number(t).toFixed(3)+'s · '+frames.length+' players'+(active.length?' · active: '+active.map(e=>e.detector_id).join(', '):'');
}
function step(n){if(!indices.length)return;cursor=Math.max(0,Math.min(indices.length-1,cursor+n));slider.value=cursor;draw()}
slider.oninput=()=>{cursor=Number(slider.value);draw()};document.getElementById('prev').onclick=()=>step(-1);document.getElementById('next').onclick=()=>step(1);document.getElementById('play').onclick=()=>{const b=document.getElementById('play');if(timer){clearInterval(timer);timer=null;b.textContent='Play';return}b.textContent='Pause';timer=setInterval(()=>{if(cursor>=indices.length-1){clearInterval(timer);timer=null;b.textContent='Play'}else step(1)},100)};
if(events.length)selectEvent(0);else{document.getElementById('details').textContent='No detection events.';draw()}
</script>
</body>
</html>`
