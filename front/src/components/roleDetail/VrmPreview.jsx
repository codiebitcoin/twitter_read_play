// ---- VRM 小预览（three.js 实时渲染）：拖拽旋转，可截图上传为角色头像 ----
import { useEffect, useRef, useState } from 'react';
import * as THREE from 'three';
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js';
import { VRMLoaderPlugin, VRMUtils } from '@pixiv/three-vrm';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';

const RENDER = 512; // 内部渲染分辨率（CSS 缩放显示，保证截图/大画布清晰）

export default function VrmPreview({ roleId, file, onUploaded }) {
  const canvasRef = useRef(null);
  const yawRef = useRef(0);          // 拖拽/自转产生的水平旋转角
  const baseYawRef = useRef(0);      // rotateVRM0 后的初始朝向（VRM0 需要补 180°）
  const hipsBaseYRef = useRef(0);    // hips 原始高度（浮动动画在其上叠加）
  const frameRef = useRef(null);     // 取景基准（头部位置），垂直拖拽移动相机用
  const camYOffRef = useRef(0);      // 垂直拖拽产生的相机高度偏移
  const zoomRef = useRef(1);         // 缩放：相机距离系数（0.5 近景 ~ 3 远景）
  const camTargetRef = useRef(new THREE.Vector3()); // 当前取景目标点（截图恢复用）
  const captureRef = useRef(null);   // 正面头像截图函数（渲染循环内挂载，独立于预览视角）
  const dragRef = useRef(null);      // 拖拽状态（起始 x/y / 起始 yaw / 起始相机偏移）
  const modelRef = useRef(null);     // 加载好的 vrm（供截图时摆姿势）
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [dragging, setDragging] = useState(false);
  const toastOk = useToastOk();
  const toastErr = useToastErr();

  const url = `/api/roles/${encodeURIComponent(roleId)}/asset?file=` + encodeURIComponent(file);

  // ---- 场景搭建 + 加载 VRM（file 变化时重建）----
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || !file) return;
    let disposed = false;
    const renderer = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: true, preserveDrawingBuffer: true });
    renderer.setSize(RENDER, RENDER, false);
    renderer.setClearColor(0x000000, 0);
    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(24, 1, 0.1, 50);
    scene.add(new THREE.AmbientLight(0xffffff, 0.9));
    const key = new THREE.DirectionalLight(0xffffff, 1.1); key.position.set(1, 2, 2); scene.add(key);

    let raf = 0;
    const clock = new THREE.Clock();
    const animate = () => {
      raf = requestAnimationFrame(animate);
      const vrm = modelRef.current;
      if (!vrm) return;
      // 待机：轻微上下浮动（在 hips 原始高度上叠加偏移，不能覆写 Y——hips.position.y
      // 本身是髋部高度 ~0.7m，覆写会让整个模型塌到地面）+ 拖拽时暂停自转
      const hips = vrm.humanoid.getNormalizedBoneNode('hips');
      if (hips) hips.position.y = hipsBaseYRef.current + Math.sin(clock.elapsedTime * 1.6) * 0.008;
      if (!dragRef.current) yawRef.current += 0.004;
      vrm.scene.rotation.y = baseYawRef.current + yawRef.current;
      // 取景：相机跟随拖拽产生的垂直偏移（camYOffRef）与缩放（zoomRef，改相机距离），模型不转时也能上下移动视角
      const f = frameRef.current;
      if (f) {
        camera.position.set(0, f.headY + 0.05 + camYOffRef.current, f.headZ + 1.35 * zoomRef.current);
        camTargetRef.current.set(0, f.headY - 0.25 + camYOffRef.current, f.headZ);
        camera.lookAt(camTargetRef.current);
      }
      vrm.update(clock.getDelta());
      renderer.render(scene, camera);
    };

    (async () => {
      try {
        const gltf = await new Promise((res, rej) => {
          new GLTFLoader().register((parser) => new VRMLoaderPlugin(parser)).load(url, res, undefined, rej);
        });
        if (disposed) return;
        const vrm = gltf.userData.vrm;
        VRMUtils.removeUnnecessaryVertices(gltf.scene);
        VRMUtils.rotateVRM0(vrm); // VRM0.x 面向 -Z，统一转到面向相机(+Z)
        baseYawRef.current = vrm.scene.rotation.y; // 记住初始朝向，自转/拖拽在其上叠加
        scene.add(gltf.scene);
        modelRef.current = vrm;
        // 记录 hips 原始高度（浮动动画在其上叠加，覆写会让模型塌到地面）
        const hips = vrm.humanoid.getNormalizedBoneNode('hips');
        hipsBaseYRef.current = hips ? hips.position.y : 0;
        // 手放下：VRM 默认 T-pose，把手臂转成自然下垂（A-pose），像正常人站立
        for (const side of ['left', 'right']) {
          const upper = vrm.humanoid.getNormalizedBoneNode(side + 'UpperArm');
          const lower = vrm.humanoid.getNormalizedBoneNode(side + 'LowerArm');
          const s = side === 'left' ? -1 : 1; // 左臂指 +X，负 Z 旋转下垂；右臂相反
          if (upper) upper.rotation.z = s * 1.15; // ~66° 下垂
          if (lower) lower.rotation.z = s * 0.25; // 肘部微弯
        }
        // 半身取景：以头部为基准摆相机，头占画面偏上位置
        const head = vrm.humanoid.getNormalizedBoneNode('head');
        const headPos = new THREE.Vector3();
        if (head) head.getWorldPosition(headPos);
        frameRef.current = { headY: headPos.y, headZ: headPos.z };
        camYOffRef.current = 0; // 换模型重置取景
        camTargetRef.current.set(headPos.x, headPos.y - 0.25, headPos.z);
        camera.position.set(0, headPos.y + 0.05, headPos.z + 1.35);
        camera.lookAt(camTargetRef.current);
        setLoading(false);
        // ---- 正面头像截图：独立于预览视角。同步完成「摆姿势→渲染→读像素→恢复」，
        //      不经过动画循环、不改 yaw/zoom/相机状态，预览画面不会闪动。----
        captureRef.current = () => {
          const savedRot = vrm.scene.rotation.y;
          const savedPos = camera.position.clone();
          const savedTgt = camTargetRef.current.clone();
          vrm.scene.rotation.y = baseYawRef.current; // 正面
          camera.position.set(0, headPos.y + 0.06, headPos.z + 0.9); // 头部特写机位
          camera.lookAt(headPos.x, headPos.y, headPos.z);
          renderer.render(scene, camera);
          const dataURL = canvas.toDataURL('image/png');
          vrm.scene.rotation.y = savedRot; // 恢复预览原状态
          camera.position.copy(savedPos);
          camera.lookAt(savedTgt);
          renderer.render(scene, camera);
          return dataURL;
        };
        animate();
      } catch (e) {
        if (!disposed) { setFailed(true); setLoading(false); }
      }
    })();

    return () => {
      disposed = true;
      cancelAnimationFrame(raf);
      const vrm = modelRef.current;
      if (vrm) { VRMUtils.deepDispose(vrm.scene); modelRef.current = null; }
      renderer.dispose();
    };
  }, [url, file]);

  // ---- 截图：调用渲染循环内挂载的正面头像截图（与预览当前视角完全无关）----
  const shot = async () => {
    if (busy) return;
    setBusy(true);
    try {
      const dataURL = captureRef.current && captureRef.current();
      if (!dataURL) throw new Error('preview not ready');
      const blob = await (await fetch(dataURL)).blob();
      const fd = new FormData();
      fd.append('file', blob, 'avatar.png');
      await api(`/api/roles/${roleId}/icon`, { method: 'POST', body: fd });
      toastOk(t('shotSaved'));
      onUploaded && onUploaded();
    } catch (e) { toastErr(e.message); }
    setBusy(false);
  };

  // ---- 拖拽：水平=旋转模型，垂直=移动相机取景高低 ----
  const onDown = (e) => { dragRef.current = { x: e.clientX, y: e.clientY, yaw: yawRef.current, off: camYOffRef.current }; setDragging(true); e.preventDefault(); };
  useEffect(() => {
    const move = (e) => {
      const d = dragRef.current;
      if (!d) return;
      yawRef.current = d.yaw + (e.clientX - d.x) * 0.012;
      camYOffRef.current = d.off - (e.clientY - d.y) * 0.003; // 向上拖：视角上移
    };
    const up = () => { if (dragRef.current) { dragRef.current = null; setDragging(false); } };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    return () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); };
  }, []);

  // ---- 缩放：按钮/滚轮改相机距离系数（0.5 近 ~ 3 远）----
  const clampZoom = (z) => Math.min(3, Math.max(0.5, z));
  const zoomStep = (f) => { zoomRef.current = clampZoom(zoomRef.current * f); };
  const resetView = () => { zoomRef.current = 1; camYOffRef.current = 0; yawRef.current = 0; };
  // 画布上滚轮缩放（非 passive 才能 preventDefault 阻止页面滚动）
  useEffect(() => {
    const c = canvasRef.current;
    if (!c) return;
    const onWheel = (e) => { e.preventDefault(); zoomStep(e.deltaY < 0 ? 0.9 : 1 / 0.9); };
    c.addEventListener('wheel', onWheel, { passive: false });
    return () => c.removeEventListener('wheel', onWheel);
  }, [file]);

  if (!file) return null;
  return (
    <div style={{ marginBottom: 8 }}>
      {/* 预览：宽 80%、高=宽 的正方形画布（内部按 512 渲染再 CSS 缩放，截图清晰） */}
      <div style={{ position: 'relative', width: '80%', aspectRatio: '1 / 1', borderRadius: 12, overflow: 'hidden', background: 'var(--bg-soft)', touchAction: 'none' }}>
        <canvas ref={canvasRef} width={RENDER} height={RENDER} style={{ width: '100%', height: '100%', display: 'block', cursor: dragging ? 'grabbing' : 'grab' }}
          onPointerDown={onDown} title={t('vrmDragHint')} />
        {loading && <div className="loading" style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>…</div>}
        {failed && <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--mut)', fontSize: 12 }}>{t('vrmPreviewFail')}</div>}
      </div>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 8, flexWrap: 'wrap' }}>
        <button className="mini" disabled={busy || loading || failed} onClick={shot}>📸 {busy ? '…' : t('shotAsIcon')}</button>
        <button className="mini ghost" title={t('zoomIn')} onClick={() => zoomStep(0.8)}>🔍＋</button>
        <button className="mini ghost" title={t('zoomOut')} onClick={() => zoomStep(1 / 0.8)}>🔍－</button>
        <button className="mini ghost" title={t('resetView')} onClick={resetView}>⟲</button>
        <span className="hint">{t('vrmDragHint')}</span>
      </div>
    </div>
  );
}
