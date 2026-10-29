// ---- VRM 缩略图渲染（three.js + @pixiv/three-vrm，失败回退占位图）----
import * as THREE from 'three';
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js';
import { VRMLoaderPlugin, VRMUtils } from '@pixiv/three-vrm';

const vrmThumbCache = {};

export async function renderVRMThumb(canvas, url) {
  if (vrmThumbCache[url]) { canvas.getContext('2d').drawImage(vrmThumbCache[url], 0, 0, canvas.width, canvas.height); return; }
  const renderer = new THREE.WebGLRenderer({ alpha: true, antialias: true });
  renderer.setSize(256, 256);
  renderer.setClearColor(0x000000, 0);
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(22, 1, 0.1, 50);
  camera.position.set(0, 1.35, 2.6);
  const light = new THREE.DirectionalLight(0xffffff, 1.1); light.position.set(1, 2, 2); scene.add(light);
  scene.add(new THREE.AmbientLight(0xffffff, 0.9));
  const gltf = await new Promise((res, rej) => {
    new GLTFLoader().register((parser) => new VRMLoaderPlugin(parser)).load(url, res, undefined, rej);
  });
  const vrm = gltf.userData.vrm;
  VRMUtils.removeUnnecessaryVertices(gltf.scene);
  VRMUtils.rotateVRM0(vrm); // VRM0.x 面向 -Z，统一转到面向相机(+Z)
  scene.add(gltf.scene);
  const head = vrm.humanoid.getNormalizedBoneNode('head');
  if (head) { head.getWorldPosition(camera.position); camera.position.z += 1.1; camera.position.y += 0.05; }
  vrm.update(0);
  renderer.render(scene, camera);
  const snap = document.createElement('canvas'); snap.width = snap.height = 256;
  snap.getContext('2d').drawImage(renderer.domElement, 0, 0);
  vrmThumbCache[url] = snap;
  canvas.getContext('2d').drawImage(snap, 0, 0, canvas.width, canvas.height);
  renderer.dispose(); VRMUtils.deepDispose(gltf.scene);
}

export function thumbPlaceholder(canvas, name) {
  const ctx = canvas.getContext('2d');
  const g = ctx.createLinearGradient(0, 0, canvas.width, canvas.height);
  g.addColorStop(0, '#1d2a44'); g.addColorStop(.6, '#2a1d44'); g.addColorStop(1, '#442a1d');
  ctx.fillStyle = g; ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.fillStyle = 'rgba(235,240,255,.85)'; ctx.font = 'bold 48px system-ui,sans-serif';
  ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
  ctx.fillText((name || '?').trim().charAt(0).toUpperCase(), canvas.width / 2, canvas.height / 2);
}
