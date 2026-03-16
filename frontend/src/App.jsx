import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ConfigProvider,
  Typography,
  Form,
  Input,
  Select,
  Collapse,
  Upload,
  Button,
  Space,
  Divider,
  Tag,
  Spin,
  Alert,
  Progress,
  Switch,
  message,
  Modal,
  Table,
  Drawer,
  InputNumber
} from 'antd';
import {
  InboxOutlined,
  RocketOutlined,
  ThunderboltOutlined,
  AppstoreOutlined,
  ClockCircleOutlined,
  PlayCircleOutlined,
  DownloadOutlined,
  PlusOutlined,
  UserOutlined
} from '@ant-design/icons';
import {
  createTask,
  getTaskStatus,
  getConfig,
  login,
  register,
  getMe,
  logout,
  getHistory,
  recharge,
  requestCaptcha,
  presignUpload,
  uploadKieImage
} from './services/api.js';
import { Stage, Layer, Image as KonvaImage, Transformer } from 'react-konva';
import './styles/app.css';

const { Title, Text } = Typography;
const { TextArea } = Input;

const MATERIAL_OPTIONS = [
  '发光字 - 无边字',
  '发光字 - 迷你字',
  '发光字 - 冲孔字',
  '发光字 - 通体字',
  '发光字 - 不锈钢背发光',
  '发光字 - 树脂字',
  '发光字 - 水晶字'
];

const LIGHT_OPTIONS = ['白光', '暖光', '冷光'];

const BOARD_OPTIONS = [
  '铝塑板',
  '亚克力板',
  '不锈钢板',
  '木纹板',
  '石材纹理板',
  '烤漆钢板'
];

const GLOW_OPTIONS = ['均匀环绕', '点阵闪烁', '柔和渐变', '高亮聚光'];

function normalizeFiles(fileList) {
  const urls = [];
  for (const file of fileList) {
    if (file.url) {
      urls.push(file.url);
      continue;
    }
    if (file.response?.file_url) {
      urls.push(file.response.file_url);
    }
  }
  return urls;
}

function clampNumber(value, min, max) {
  return Math.min(max, Math.max(min, value));
}

function inferImageExtByType(type) {
  const mime = String(type || '').toLowerCase();
  if (mime === 'image/jpeg' || mime === 'image/jpg') return 'jpg';
  if (mime === 'image/png') return 'png';
  if (mime === 'image/webp') return 'webp';
  if (mime === 'image/gif') return 'gif';
  if (mime === 'image/bmp') return 'bmp';
  if (mime === 'image/avif') return 'avif';
  return '';
}

function buildUploadPayloadFile(file) {
  const safeType = file?.type || 'application/octet-stream';
  const originalName = String(file?.name || '').trim();
  const ext = inferImageExtByType(safeType);
  const fallbackName = `upload_${Date.now().toString(36)}${ext ? `.${ext}` : ''}`;
  return {
    uploadFile: file,
    filename: originalName || fallbackName,
    contentType: safeType
  };
}

function readFileAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(new Error('读取图片失败'));
    reader.readAsDataURL(file);
  });
}

function isDataURL(value) {
  return typeof value === 'string' && value.startsWith('data:image/');
}

function inferImageExtByURL(rawURL) {
  const clean = String(rawURL || '').trim().split('#')[0].split('?')[0];
  const match = clean.match(/\.([a-zA-Z0-9]+)$/);
  if (!match) return '';
  const ext = match[1].toLowerCase();
  const allowed = new Set(['jpg', 'jpeg', 'png', 'webp', 'gif', 'bmp', 'avif']);
  return allowed.has(ext) ? ext : '';
}

function buildExportFilename(prefix, rawURL, index) {
  const safePrefix = String(prefix || 'result')
    .trim()
    .replace(/[^a-zA-Z0-9_-]+/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_+|_+$/g, '') || 'result';
  const ext = inferImageExtByURL(rawURL) || 'png';
  const stamp = new Date().toISOString().replace(/[-:.TZ]/g, '').slice(0, 14);
  const suffix = typeof index === 'number' && index >= 0 ? `_${index + 1}` : '';
  return `${safePrefix}_${stamp}${suffix}.${ext}`;
}

function resolveImageURL(entry) {
  if (!entry) return '';
  if (typeof entry === 'string') return entry;
  const url = entry.url;
  if (typeof url === 'string') return url;
  if (Array.isArray(url) && url.length > 0 && typeof url[0] === 'string') return url[0];
  return '';
}

function useCanvasImage(src) {
  const [image, setImage] = useState(null);

  useEffect(() => {
    if (!src) {
      setImage(null);
      return;
    }
    const img = new window.Image();
    img.onload = () => setImage(img);
    img.onerror = () => {
      console.warn('[editor] image load failed:', src);
      setImage(null);
    };
    img.src = src;
    return () => {
      img.onload = null;
      img.onerror = null;
    };
  }, [src]);

  return image;
}

const DEFAULT_CANVAS_ASPECT = 16 / 9;

function computeOverlaySize(placement, canvasAspect, imageAspect, lockAspect) {
  const safeCanvasAspect = canvasAspect > 0 ? canvasAspect : DEFAULT_CANVAS_ASPECT;
  const safeImageAspect = imageAspect > 0 ? imageAspect : 1;
  let width = clampNumber(Number(placement.designW) || 1, 1, 100);
  let height = clampNumber(Number(placement.designH) || 1, 1, 100);

  if (lockAspect) {
    height = width * (safeCanvasAspect / safeImageAspect);
    if (height > 100) {
      height = 100;
      width = height * (safeImageAspect / safeCanvasAspect);
    }
  }
  return {
    width: Number(width.toFixed(2)),
    height: Number(height.toFixed(2))
  };
}

function normalizePlacement(placement, canvasAspect, imageAspect, lockAspect) {
  const size = computeOverlaySize(placement, canvasAspect, imageAspect, lockAspect);
  const safeX = clampNumber(Number(placement.designX) || 0, 0, Math.max(0, 100 - size.width));
  const safeY = clampNumber(Number(placement.designY) || 0, 0, Math.max(0, 100 - size.height));
  const safeStyleWeight = clampNumber(Number(placement.styleWeight) || 0, 0, 100);
  return {
    ...placement,
    designX: Number(safeX.toFixed(2)),
    designY: Number(safeY.toFixed(2)),
    designW: size.width,
    designH: size.height,
    styleWeight: Number(safeStyleWeight.toFixed(2))
  };
}

function isPlacementEqual(a, b) {
  return a.designX === b.designX
    && a.designY === b.designY
    && a.designW === b.designW
    && a.designH === b.designH
    && a.styleWeight === b.styleWeight;
}

function App() {
  const [form] = Form.useForm();
  const [designFiles, setDesignFiles] = useState([]);
  const [sceneFiles, setSceneFiles] = useState([]);
  const [refFiles, setRefFiles] = useState([]);
  const [submitting, setSubmitting] = useState(false);
  const [taskInfo, setTaskInfo] = useState(null);
  const [polling, setPolling] = useState(false);
  const [error, setError] = useState(null);
  const [currentTab, setCurrentTab] = useState('product');
  const [authVisible, setAuthVisible] = useState(false);
  const [authMode, setAuthMode] = useState('login');
  const [user, setUser] = useState(null);
  const [history, setHistory] = useState([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [selectedHistory, setSelectedHistory] = useState(null);
  const [rechargeVisible, setRechargeVisible] = useState(false);
  const [rechargeAmount, setRechargeAmount] = useState(20);
  const [captchaEnabled, setCaptchaEnabled] = useState(false);
  const [aiProvider, setAiProvider] = useState('legacy');
  const [captchaLoading, setCaptchaLoading] = useState(false);
  const [captchaCountdown, setCaptchaCountdown] = useState(0);
  const [placement, setPlacement] = useState({
    designX: 18,
    designY: 24,
    designW: 62,
    designH: 30,
    styleWeight: 72
  });
  const [editorPanelOpen, setEditorPanelOpen] = useState(true);
  const [lockAspect, setLockAspect] = useState(false);
  const [designAspect, setDesignAspect] = useState(1);
  const [canvasAspect, setCanvasAspect] = useState(DEFAULT_CANVAS_ASPECT);
  const [stageSize, setStageSize] = useState({ width: 0, height: 0 });
  const [authForm] = Form.useForm();
  const editorCanvasRef = useRef(null);
  const designNodeRef = useRef(null);
  const transformerRef = useRef(null);
  const canvasAspectRef = useRef(DEFAULT_CANVAS_ASPECT);
  const designAspectRef = useRef(1);
  const lockAspectRef = useRef(false);

  const sceneImage = useMemo(() => normalizeFiles(sceneFiles)[0] || '', [sceneFiles]);
  const designImage = useMemo(() => normalizeFiles(designFiles)[0] || '', [designFiles]);
  const styleImage = useMemo(() => normalizeFiles(refFiles)[0] || '', [refFiles]);
  const sceneCanvasImage = useCanvasImage(sceneImage);
  const designCanvasImage = useCanvasImage(designImage);

  const overlaySize = useMemo(
    () => computeOverlaySize(placement, canvasAspect, designAspect, lockAspect),
    [placement, canvasAspect, designAspect, lockAspect]
  );

  const placementHint = useMemo(() => {
    const lines = ['【图像定位参数】'];
    if (sceneImage && designImage) {
      lines.push('以第 1 张门头/现场图为基准坐标（左上角为原点，单位为百分比）。');
      lines.push(
        `将第 2 张平面设计图中的文字与主视觉放置到：x=${placement.designX}%，y=${placement.designY}%，宽=${overlaySize.width}%，高=${overlaySize.height}%。`
      );
      lines.push(lockAspect ? '第 2 张图保持原始纵横比缩放。' : '第 2 张图允许手动变形拉伸，请优先按定位参数执行。');
      lines.push('仅在门头区域贴图，不要覆盖天空、地面、路人等无关区域。');
    } else {
      lines.push('当前未同时提供第 1 张门头图与第 2 张平面图，请按已上传图片自动判断。');
    }
    if (styleImage) {
      lines.push(`第 3 张效果图仅用于风格参考，风格权重约 ${placement.styleWeight}%，不要照搬其文案内容。`);
    }
    return lines.join('\n');
  }, [sceneImage, designImage, styleImage, placement, overlaySize, lockAspect]);

  const promptSummary = useMemo(() => {
    const parts = [];
    if (form.getFieldValue('slogan')) parts.push('标语');
    if (form.getFieldValue('material')) parts.push('耗材');
    if (form.getFieldValue('board')) parts.push('底板');
    if (form.getFieldValue('glow')) parts.push('光效');
    return parts.length ? `已配置：${parts.join(' / ')}` : '已配置：基础参数';
  }, [form]);

  useEffect(() => {
    const load = async () => {
      try {
        const cfg = await getConfig();
        setCaptchaEnabled(!!cfg.captcha_enabled);
        setAiProvider(cfg?.ai_provider || 'legacy');
        const me = await getMe();
        setUser(me);
      } catch {
        setUser(null);
      }
    };
    load();
  }, []);

  useEffect(() => {
    if (currentTab === 'history') {
      fetchHistory();
    }
  }, [currentTab]);

  useEffect(() => {
    if (captchaCountdown <= 0) return;
    const timer = setTimeout(() => setCaptchaCountdown((v) => v - 1), 1000);
    return () => clearTimeout(timer);
  }, [captchaCountdown]);

  useEffect(() => {
    designAspectRef.current = designAspect;
  }, [designAspect]);

  useEffect(() => {
    canvasAspectRef.current = canvasAspect;
  }, [canvasAspect]);

  useEffect(() => {
    lockAspectRef.current = lockAspect;
  }, [lockAspect]);

  useEffect(() => {
    if (!designImage) {
      setDesignAspect(1);
      return;
    }
    const img = new Image();
    img.onload = () => {
      const nextAspect = img.naturalWidth && img.naturalHeight ? img.naturalWidth / img.naturalHeight : 1;
      setDesignAspect(nextAspect > 0 ? nextAspect : 1);
    };
    img.onerror = () => setDesignAspect(1);
    img.src = designImage;
  }, [designImage]);

  useEffect(() => {
    const node = editorCanvasRef.current;
    if (!node) return undefined;

    const updateCanvasMetrics = () => {
      const rect = node.getBoundingClientRect();
      if (rect.width > 0 && rect.height > 0) {
        const nextWidth = Math.round(rect.width);
        const nextHeight = Math.round(rect.height);
        setStageSize((prev) => {
          if (prev.width === nextWidth && prev.height === nextHeight) {
            return prev;
          }
          return { width: nextWidth, height: nextHeight };
        });
        const nextAspect = nextWidth > 0 && nextHeight > 0 ? nextWidth / nextHeight : DEFAULT_CANVAS_ASPECT;
        setCanvasAspect((prev) => (Math.abs(prev - nextAspect) < 0.0001 ? prev : nextAspect));
      }
    };

    updateCanvasMetrics();

    if (typeof ResizeObserver !== 'undefined') {
      const observer = new ResizeObserver(updateCanvasMetrics);
      observer.observe(node);
      return () => observer.disconnect();
    }

    window.addEventListener('resize', updateCanvasMetrics);
    return () => window.removeEventListener('resize', updateCanvasMetrics);
  }, [currentTab]);

  useEffect(() => {
    setPlacement((prev) => {
      const normalized = normalizePlacement(prev, canvasAspect, designAspect, lockAspect);
      return isPlacementEqual(prev, normalized) ? prev : normalized;
    });
  }, [canvasAspect, designAspect, lockAspect]);

  const fetchHistory = async () => {
    setHistoryLoading(true);
    try {
      const list = await getHistory();
      setHistory(list);

      const pendingTasks = list
        .filter((item) => {
          const status = String(item?.status || '').toUpperCase();
          return status !== 'SUCCESS' && status !== 'FAILED';
        })
        .slice(0, 6);

      if (pendingTasks.length > 0) {
        void (async () => {
          await Promise.allSettled(
            pendingTasks.map((item) => getTaskStatus(item.task_id))
          );
          try {
            const refreshed = await getHistory();
            setHistory(refreshed);
          } catch {
            // Ignore background refresh errors, keep initial history list.
          }
        })();
      }
    } catch (err) {
      message.error(err?.response?.data?.error || '加载历史失败');
    } finally {
      setHistoryLoading(false);
    }
  };

  const handleSubmit = async (values) => {
    if (!user) {
      setAuthVisible(true);
      return;
    }
    setError(null);
    setEditorPanelOpen(false);
    setSubmitting(true);
    setTaskInfo(null);

    try {
      const designImages = normalizeFiles(designFiles);
      const sceneImages = normalizeFiles(sceneFiles);
      const refImages = normalizeFiles(refFiles);

      const imageList = [sceneImages[0], designImages[0], refImages[0]].filter(Boolean);
      if (imageList.length > 3) {
        throw new Error('图片总数不能超过 3 张');
      }

      const mergedDetails = [values.details, placementHint].filter(Boolean).join('\n');

      const payload = {
        slogan: values.slogan,
        material: values.material,
        lightColor: values.lightColor,
        board: values.board,
        glow: values.glow,
        details: mergedDetails,
        resolution: values.resolution || '1K',
        image_list: imageList.length ? imageList : undefined
      };

      const createRes = await createTask(payload);
      setTaskInfo({
        ...createRes,
        status: createRes.status || 'PENDING',
        progress: 10
      });

      if (user) {
        setUser({ ...user, credits: user.credits - 3 });
      }

      setPolling(true);
      let done = false;
      let progress = 10;
      while (!done) {
        await new Promise((r) => setTimeout(r, 2000));
        const statusRes = await getTaskStatus(createRes.task_id);
        progress = Math.min(progress + 10, 90);
        if (statusRes.status === 'SUCCESS') {
          done = true;
          setTaskInfo({ ...statusRes, progress: 100 });
        } else if (statusRes.status === 'FAILED') {
          done = true;
          setTaskInfo({ ...statusRes, progress: 100 });
        } else {
          setTaskInfo({ ...statusRes, progress });
        }
      }
    } catch (err) {
      setError(err?.response?.data?.error || err.message || '请求失败');
    } finally {
      setSubmitting(false);
      setPolling(false);
    }
  };

  const isEditingLocked = submitting || polling;
  const showResultPanel = isEditingLocked || !!taskInfo || !!error;
  const editorCollapseActiveKeys = editorPanelOpen ? ['editor-panel'] : [];
  const transformerAnchors = useMemo(
    () => (lockAspect
      ? ['top-left', 'top-right', 'bottom-left', 'bottom-right']
      : ['top-left', 'top-center', 'top-right', 'middle-right', 'middle-left', 'bottom-left', 'bottom-center', 'bottom-right']
    ),
    [lockAspect]
  );

  const handleExportImage = useCallback(async (url, prefix, index) => {
    const imageURL = String(url || '').trim();
    if (!imageURL) {
      message.warning('没有可导出的图片');
      return;
    }
    const filename = buildExportFilename(prefix, imageURL, index);

    const triggerDownload = (href) => {
      const link = document.createElement('a');
      link.href = href;
      link.download = filename;
      link.rel = 'noopener noreferrer';
      link.style.display = 'none';
      document.body.appendChild(link);
      link.click();
      link.remove();
    };

    try {
      const response = await fetch(imageURL);
      if (!response.ok) {
        throw new Error(`下载失败 (${response.status})`);
      }
      const blob = await response.blob();
      const objectURL = URL.createObjectURL(blob);
      triggerDownload(objectURL);
      URL.revokeObjectURL(objectURL);
      message.success('导出成功');
    } catch (err) {
      triggerDownload(imageURL);
      message.info('已触发导出；如浏览器拦截，可右键另存为');
    }
  }, []);

  const uploadProps = {
    accept: 'image/png,image/jpeg,image/webp',
    beforeUpload: (file) => {
      if (!user) {
        message.warning('请先登录再上传图片');
        setAuthVisible(true);
        return Upload.LIST_IGNORE;
      }
      const okTypes = ['image/jpeg', 'image/png', 'image/webp'];
      if (!okTypes.includes(file.type)) {
        message.error('仅支持 JPG/PNG/WebP 图片格式');
        return Upload.LIST_IGNORE;
      }
      const maxSizeMB = aiProvider === 'apimart' ? 10 : 5;
      if (file.size / 1024 / 1024 > maxSizeMB) {
        message.error(`图片大小不能超过 ${maxSizeMB}MB`);
        return Upload.LIST_IGNORE;
      }
      return true;
    },
    customRequest: async ({ file, onSuccess, onError }) => {
      try {
        const { uploadFile, filename, contentType } = buildUploadPayloadFile(file);
        if (aiProvider === 'kie') {
          const dataUrl = await readFileAsDataURL(uploadFile);
          if (typeof dataUrl !== 'string' || !dataUrl.startsWith('data:')) {
            throw new Error('图片编码失败');
          }
          const uploaded = await uploadKieImage({
            filename,
            content_type: contentType,
            data_url: dataUrl
          });
          onSuccess({ file_url: uploaded.file_url, key: uploaded.key });
          return;
        }
        if (aiProvider === 'apimart') {
          const dataUrl = await readFileAsDataURL(uploadFile);
          if (!isDataURL(dataUrl)) {
            throw new Error('图片编码失败');
          }
          onSuccess({ file_url: dataUrl, key: filename });
          return;
        }

        const presign = await presignUpload({
          filename,
          content_type: contentType
        });
        const uploadRes = await fetch(presign.upload_url, {
          method: 'PUT',
          headers: {
            'Content-Type': contentType
          },
          body: uploadFile
        });
        if (!uploadRes.ok) {
          const errText = (await uploadRes.text()).slice(0, 300);
          throw new Error(`S3 上传失败 (${uploadRes.status})${errText ? `: ${errText}` : ''}`);
        }
        onSuccess({ file_url: presign.file_url, key: presign.key });
      } catch (err) {
        onError(err);
        message.error(err?.message || '上传失败，请重试');
      }
    },
    multiple: false,
    maxCount: 1,
    listType: 'picture'
  };

  const updatePlacement = useCallback((nextOrUpdater) => {
    setPlacement((prev) => {
      const next = typeof nextOrUpdater === 'function' ? nextOrUpdater(prev) : nextOrUpdater;
      const normalized = normalizePlacement(next, canvasAspectRef.current, designAspectRef.current, lockAspectRef.current);
      return isPlacementEqual(prev, normalized) ? prev : normalized;
    });
  }, []);

  const setPlacementField = (field, min, max) => (nextVal) => {
    if (typeof nextVal !== 'number' || Number.isNaN(nextVal)) {
      return;
    }
    updatePlacement((prev) => ({ ...prev, [field]: clampNumber(nextVal, min, max) }));
  };

  const stageWidth = stageSize.width > 0 ? stageSize.width : 1;
  const stageHeight = stageSize.height > 0 ? stageSize.height : 1;

  const overlayRectPx = useMemo(() => ({
    x: (placement.designX / 100) * stageWidth,
    y: (placement.designY / 100) * stageHeight,
    width: (overlaySize.width / 100) * stageWidth,
    height: (overlaySize.height / 100) * stageHeight
  }), [
    placement.designX,
    placement.designY,
    overlaySize.width,
    overlaySize.height,
    stageWidth,
    stageHeight
  ]);

  const handleOverlayDragEnd = useCallback((event) => {
    if (isEditingLocked) {
      return;
    }
    if (stageSize.width <= 0 || stageSize.height <= 0) {
      return;
    }
    const node = event.target;
    const width = node.width();
    const height = node.height();
    const maxX = Math.max(0, stageSize.width - width);
    const maxY = Math.max(0, stageSize.height - height);
    const nextX = clampNumber(node.x(), 0, maxX);
    const nextY = clampNumber(node.y(), 0, maxY);

    node.position({ x: nextX, y: nextY });
    updatePlacement((prev) => ({
      ...prev,
      designX: Number(((nextX / stageSize.width) * 100).toFixed(2)),
      designY: Number(((nextY / stageSize.height) * 100).toFixed(2))
    }));
  }, [isEditingLocked, stageSize.width, stageSize.height, updatePlacement]);

  const handleOverlayTransformEnd = useCallback(() => {
    if (isEditingLocked) {
      return;
    }
    if (!designNodeRef.current || stageSize.width <= 0 || stageSize.height <= 0) {
      return;
    }
    const node = designNodeRef.current;
    const scaleX = node.scaleX();
    const scaleY = node.scaleY();
    let nextWidth = Math.max(24, node.width() * scaleX);
    let nextHeight = Math.max(24, node.height() * scaleY);

    if (lockAspect) {
      const ratio = designAspect > 0 ? designAspect : 1;
      nextHeight = nextWidth / ratio;
      if (nextWidth > stageSize.width) {
        nextWidth = stageSize.width;
        nextHeight = nextWidth / ratio;
      }
      if (nextHeight > stageSize.height) {
        nextHeight = stageSize.height;
        nextWidth = nextHeight * ratio;
      }
    } else {
      nextWidth = Math.min(nextWidth, stageSize.width);
      nextHeight = Math.min(nextHeight, stageSize.height);
    }

    const maxX = Math.max(0, stageSize.width - nextWidth);
    const maxY = Math.max(0, stageSize.height - nextHeight);
    const nextX = clampNumber(node.x(), 0, maxX);
    const nextY = clampNumber(node.y(), 0, maxY);

    node.scaleX(1);
    node.scaleY(1);
    node.width(nextWidth);
    node.height(nextHeight);
    node.position({ x: nextX, y: nextY });

    updatePlacement((prev) => ({
      ...prev,
      designX: Number(((nextX / stageSize.width) * 100).toFixed(2)),
      designY: Number(((nextY / stageSize.height) * 100).toFixed(2)),
      designW: Number(((nextWidth / stageSize.width) * 100).toFixed(2)),
      designH: Number(((nextHeight / stageSize.height) * 100).toFixed(2))
    }));
  }, [designAspect, lockAspect, isEditingLocked, stageSize.width, stageSize.height, updatePlacement]);

  const handleTransformBound = useCallback((oldBox, newBox) => {
    if (stageSize.width <= 0 || stageSize.height <= 0) {
      return oldBox;
    }
    const minSize = 24;
    let width = clampNumber(Math.abs(newBox.width), minSize, stageSize.width);
    let height = clampNumber(Math.abs(newBox.height), minSize, stageSize.height);

    if (lockAspect) {
      const ratio = designAspect > 0 ? designAspect : 1;
      if (width / height > ratio) {
        width = height * ratio;
      } else {
        height = width / ratio;
      }
      width = Math.min(width, stageSize.width);
      height = Math.min(height, stageSize.height);
    }

    const x = clampNumber(newBox.x, 0, Math.max(0, stageSize.width - width));
    const y = clampNumber(newBox.y, 0, Math.max(0, stageSize.height - height));
    return {
      ...newBox,
      x,
      y,
      width,
      height
    };
  }, [stageSize.width, stageSize.height, lockAspect, designAspect]);

  useEffect(() => {
    if (!transformerRef.current) {
      return;
    }
    if (sceneCanvasImage && designCanvasImage && designNodeRef.current) {
      transformerRef.current.nodes([designNodeRef.current]);
    } else {
      transformerRef.current.nodes([]);
    }
    transformerRef.current.getLayer()?.batchDraw();
  }, [
    sceneCanvasImage,
    designCanvasImage,
    overlayRectPx.x,
    overlayRectPx.y,
    overlayRectPx.width,
    overlayRectPx.height,
    lockAspect
  ]);

  const normalizeFileListState = (fileList) =>
    fileList.map((file) => {
      if (!file.url && file.response?.file_url) {
        return { ...file, url: file.response.file_url };
      }
      return file;
    });

  const handleAuth = async (values) => {
    try {
      const res = authMode === 'login' ? await login(values) : await register(values);
      setUser(res);
      setAuthVisible(false);
      message.success(authMode === 'login' ? '登录成功' : '注册成功');
    } catch (err) {
      message.error(err?.response?.data?.error || '登录失败');
    }
  };

  const handleSendCaptcha = async () => {
    const phone = authForm.getFieldValue('phone');
    if (!phone) {
      message.warning('请先输入手机号');
      return;
    }
    setCaptchaLoading(true);
    try {
      const res = await requestCaptcha({ phone });
      if (res?.dev_code) {
        message.info(`开发模式验证码：${res.dev_code}`);
      } else {
        message.success('验证码已发送');
      }
      setCaptchaCountdown(60);
    } catch (err) {
      message.error(err?.response?.data?.error || '发送失败');
    } finally {
      setCaptchaLoading(false);
    }
  };

  const handleLogout = async () => {
    await logout();
    setUser(null);
    message.success('已退出');
  };

  const handleRecharge = async () => {
    try {
      const res = await recharge({ amount: rechargeAmount });
      setUser(res);
      setRechargeVisible(false);
      message.success('充值成功');
    } catch (err) {
      message.error(err?.response?.data?.error || '充值失败');
    }
  };

  const historyColumns = [
    {
      title: '任务 ID',
      dataIndex: 'task_id',
      key: 'task_id',
      width: 200,
      render: (val) => <span className="mono">{val}</span>
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 120,
      render: (val) => <Tag color={val === 'SUCCESS' ? 'green' : val === 'FAILED' ? 'red' : 'blue'}>{val}</Tag>
    },
    {
      title: '生成时间',
      dataIndex: 'created_at',
      key: 'created_at',
      render: (val) => new Date(val).toLocaleString()
    },
    {
      title: '预览',
      dataIndex: 'images',
      key: 'images',
      render: (imgs) => (imgs && imgs.length ? <img src={imgs[0]} alt="thumb" className="thumb" /> : '-'),
      width: 120
    },
    {
      title: '操作',
      key: 'action',
      render: (_, record) => (
        <Space>
          <Button size="small" onClick={() => setSelectedHistory(record)}>查看</Button>
          {record.images && record.images.length > 0 && (
            <Button size="small" type="link" href={record.images[0]} target="_blank" download>
              下载
            </Button>
          )}
        </Space>
      )
    }
  ];

  return (
    <ConfigProvider
      theme={{
        token: {
          fontFamily: '"PingFang SC", "Hiragino Sans GB", "Source Han Sans SC", "Microsoft YaHei", sans-serif',
          colorPrimary: '#86d34d',
          borderRadius: 12,
          colorBgLayout: '#f3f5f8'
        }
      }}
    >
      <div className="page">
        <header className="topbar">
          <div className="topbar-left">
            <div className="brand-pill">
              <div className="brand-icon">A</div>
              <div>
                <Title level={5} style={{ margin: 0 }}>AI 广告生成器</Title>
                <Text type="secondary">招牌效果图生成</Text>
              </div>
            </div>
            <RocketOutlined className="icon-muted" />
            <ThunderboltOutlined className="icon-muted" />
          </div>
          <div className="topbar-right">
            <Tag color="gold">{user ? `${user.credits} 积分` : '未登录'}</Tag>
            <Button size="small" icon={<PlusOutlined />} onClick={() => setRechargeVisible(true)} disabled={!user}>充值</Button>
            <div className="divider-vert" />
            <Select size="small" defaultValue="1x" options={[{ value: '1x' }, { value: '2x' }]} />
            {user ? (
              <Button size="small" icon={<UserOutlined />} onClick={handleLogout}>退出</Button>
            ) : (
              <Button size="small" onClick={() => setAuthVisible(true)}>登录</Button>
            )}
          </div>
        </header>

        <div className="workspace">
          <aside className="sider">
            <div className={`sider-item ${currentTab === 'product' ? 'active' : ''}`} onClick={() => setCurrentTab('product')}>
              <AppstoreOutlined />
              <span>产品</span>
            </div>
            <div className={`sider-item ${currentTab === 'history' ? 'active' : ''}`} onClick={() => setCurrentTab('history')}>
              <ClockCircleOutlined />
              <span>历史</span>
            </div>
          </aside>

          <main className={`main ${currentTab === 'history' ? 'history-main' : ''}`}>
            {currentTab === 'product' && (
              <section className="left-panel">
                <div className="panel-card">
                  <div className="panel-title">
                    <Title level={5} style={{ margin: 0 }}>产品</Title>
                    <Tag color="green">每张图 3 积分</Tag>
                  </div>

                  <Form
                    layout="vertical"
                    form={form}
                    className="product-form"
                    onFinish={handleSubmit}
                    initialValues={{
                      resolution: '2K',
                      lightColor: '白光'
                    }}
                  >
                    <div className="product-form-scroll">
                      <Form.Item label="门头/现场照片（图 1）">
                        <Upload.Dragger
                        {...uploadProps}
                        fileList={sceneFiles}
                        onChange={({ fileList }) => setSceneFiles(normalizeFileListState(fileList))}
                        className="upload-green"
                      >
                          <p className="ant-upload-drag-icon">
                            <InboxOutlined />
                          </p>
                          <p className="ant-upload-text">上传</p>
                          <p className="ant-upload-hint">或将图像拖放到此处</p>
                        </Upload.Dragger>
                      </Form.Item>

                      <div className="mini-cards">
                        <div className="mini-card">
                          <Text type="secondary">分辨率</Text>
                          <Form.Item name="resolution" noStyle>
                            <Select
                              size="small"
                              options={[{ value: '1K' }, { value: '2K' }, { value: '4K' }]}
                            />
                          </Form.Item>
                        </div>
                        <div className="mini-card">
                          <Text type="secondary">灯颜色</Text>
                          <Form.Item name="lightColor" noStyle>
                            <Select size="small" options={LIGHT_OPTIONS.map((v) => ({ value: v }))} />
                          </Form.Item>
                        </div>
                      </div>

                      <Form.Item label="平面设计图（图 2）">
                        <Upload.Dragger
                        {...uploadProps}
                        fileList={designFiles}
                        onChange={({ fileList }) => setDesignFiles(normalizeFileListState(fileList))}
                        className="upload-muted"
                      >
                          <p className="ant-upload-drag-icon">
                            <InboxOutlined />
                          </p>
                          <p className="ant-upload-text">上传</p>
                          <p className="ant-upload-hint">或将图像拖放到此处</p>
                        </Upload.Dragger>
                      </Form.Item>

                      <Divider />

                      <Collapse
                        className="form-collapse"
                        bordered={false}
                        defaultActiveKey={['copy']}
                        items={[
                          {
                            key: 'copy',
                            label: '文案与材质设置',
                            children: (
                              <>
                                <Form.Item
                                  name="slogan"
                                  label="广告标语"
                                >
                                  <Input placeholder="如：焕新开业，限时九折" />
                                </Form.Item>

                                <Form.Item name="material" label="广告耗材">
                                  <Select placeholder="选择发光字类型" options={MATERIAL_OPTIONS.map((v) => ({ value: v }))} />
                                </Form.Item>

                                <Form.Item name="board" label="底板">
                                  <Select placeholder="选择底板材质" options={BOARD_OPTIONS.map((v) => ({ value: v }))} />
                                </Form.Item>

                                <Form.Item name="glow" label="光样式">
                                  <Select placeholder="选择光样式" options={GLOW_OPTIONS.map((v) => ({ value: v }))} />
                                </Form.Item>

                                <Form.Item name="details" label="更多细节描述" style={{ marginBottom: 0 }}>
                                  <TextArea rows={3} placeholder="如：主色为深蓝，门头宽 6 米，高 1.2 米" />
                                </Form.Item>
                              </>
                            )
                          },
                          {
                            key: 'style',
                            label: '参考效果图（图 3）',
                            children: (
                              <>
                                <Form.Item label="效果风格图上传" style={{ marginBottom: 8 }}>
                                  <Upload.Dragger
                                  {...uploadProps}
                                  fileList={refFiles}
                                  onChange={({ fileList }) => setRefFiles(normalizeFileListState(fileList))}
                                >
                                    <p className="ant-upload-drag-icon">
                                      <InboxOutlined />
                                    </p>
                                    <p className="ant-upload-text">上传</p>
                                    <p className="ant-upload-hint">或将图像拖放到此处</p>
                                  </Upload.Dragger>
                                </Form.Item>
                                <Text type="secondary">提示：三类图片各上传 1 张，按图 1/2/3 顺序解析。</Text>
                              </>
                            )
                          }
                        ]}
                      />

                      <div className="prompt-summary">{promptSummary}</div>
                    </div>
                    <div className="product-form-actions">
                      <Button
                        type="primary"
                        htmlType="submit"
                        loading={submitting}
                        size="large"
                        icon={<PlayCircleOutlined />}
                        block
                      >
                        生成
                      </Button>
                    </div>
                  </Form>
                </div>
              </section>
            )}

            <section className={`right-panel ${currentTab === 'history' ? 'history-only' : ''}`}>
              {currentTab === 'product' && (
                <div className={`product-right-stack ${showResultPanel ? 'has-result' : 'editor-only'}`}>
                  <div className={`editor-card ${showResultPanel ? 'compact' : 'expanded'} ${editorPanelOpen ? 'panel-open' : 'panel-closed'} ${isEditingLocked ? 'locked' : ''}`}>
                    <div className="editor-header">
                      <Title level={5} style={{ margin: 0 }}>定位编辑区</Title>
                      <Tag color={isEditingLocked ? 'orange' : 'blue'}>
                        {isEditingLocked ? '生成中，编辑已锁定' : '将自动写入提示词'}
                      </Tag>
                    </div>
                    <Collapse
                      className="editor-collapse"
                      bordered={false}
                      activeKey={editorCollapseActiveKeys}
                      onChange={(nextKeys) => {
                        if (isEditingLocked) {
                          return;
                        }
                        const keys = Array.isArray(nextKeys) ? nextKeys : [nextKeys];
                        setEditorPanelOpen(keys.includes('editor-panel'));
                      }}
                      items={[
                        {
                          key: 'editor-panel',
                          label: '画布与定位参数',
                          collapsible: isEditingLocked ? 'disabled' : 'header',
                          children: (
                            <>
                              <div className={`editor-canvas ${isEditingLocked ? 'disabled' : ''}`} ref={editorCanvasRef}>
                                {sceneImage ? (
                                  stageSize.width > 0 && stageSize.height > 0 ? (
                                    <Stage width={stageWidth} height={stageHeight}>
                                      <Layer>
                                        {sceneCanvasImage && (
                                          <KonvaImage
                                            image={sceneCanvasImage}
                                            x={0}
                                            y={0}
                                            width={stageWidth}
                                            height={stageHeight}
                                            listening={false}
                                          />
                                        )}
                                        {designCanvasImage && (
                                          <KonvaImage
                                            ref={designNodeRef}
                                            image={designCanvasImage}
                                            x={overlayRectPx.x}
                                            y={overlayRectPx.y}
                                            width={overlayRectPx.width}
                                            height={overlayRectPx.height}
                                            draggable={!isEditingLocked}
                                            onDragEnd={handleOverlayDragEnd}
                                            onTransformEnd={handleOverlayTransformEnd}
                                            onMouseEnter={(event) => {
                                              const container = event.target.getStage()?.container();
                                              if (container) container.style.cursor = isEditingLocked ? 'not-allowed' : 'move';
                                            }}
                                            onMouseLeave={(event) => {
                                              const container = event.target.getStage()?.container();
                                              if (container) container.style.cursor = 'default';
                                            }}
                                            opacity={0.98}
                                          />
                                        )}
                                        {sceneCanvasImage && designCanvasImage && !isEditingLocked && (
                                          <Transformer
                                            ref={transformerRef}
                                            rotateEnabled={false}
                                            flipEnabled={false}
                                            keepRatio={lockAspect}
                                            shiftBehavior="none"
                                            enabledAnchors={transformerAnchors}
                                            boundBoxFunc={handleTransformBound}
                                            borderStroke="#86d34d"
                                            borderStrokeWidth={1.5}
                                            anchorStroke="#86d34d"
                                            anchorFill="#ffffff"
                                            anchorSize={10}
                                          />
                                        )}
                                      </Layer>
                                    </Stage>
                                  ) : (
                                    <div className="editor-placeholder">加载编辑器中...</div>
                                  )
                                ) : (
                                  <div className="editor-placeholder">上传图 1 后在此预览定位</div>
                                )}
                                {isEditingLocked && <div className="editor-lock-mask">正在生成，定位编辑已锁定</div>}
                              </div>

                              <div className="editor-grid">
                                <div className="editor-item">
                                  <Text type="secondary">X (%)</Text>
                                  <InputNumber disabled={isEditingLocked} min={0} max={100} value={placement.designX} onChange={setPlacementField('designX', 0, 100)} />
                                </div>
                                <div className="editor-item">
                                  <Text type="secondary">Y (%)</Text>
                                  <InputNumber disabled={isEditingLocked} min={0} max={100} value={placement.designY} onChange={setPlacementField('designY', 0, 100)} />
                                </div>
                                <div className="editor-item">
                                  <Text type="secondary">宽 (%)</Text>
                                  <InputNumber disabled={isEditingLocked} min={1} max={100} value={placement.designW} onChange={setPlacementField('designW', 1, 100)} />
                                </div>
                                <div className="editor-item">
                                  <Text type="secondary">{lockAspect ? '自动高 (%)' : '高 (%)'}</Text>
                                  {lockAspect ? (
                                    <Text>{overlaySize.height}</Text>
                                  ) : (
                                    <InputNumber disabled={isEditingLocked} min={1} max={100} value={placement.designH} onChange={setPlacementField('designH', 1, 100)} />
                                  )}
                                </div>
                                <div className="editor-item">
                                  <Text type="secondary">保持比例</Text>
                                  <Switch
                                    disabled={isEditingLocked}
                                    checked={lockAspect}
                                    onChange={(checked) => {
                                      setLockAspect(checked);
                                      lockAspectRef.current = checked;
                                      updatePlacement((prev) => ({ ...prev }));
                                    }}
                                  />
                                  <Text type="secondary" style={{ display: 'block', marginTop: 6 }}>
                                    {lockAspect ? '当前为等比缩放' : '当前可自由变形（拖动边缘锚点可单独拉伸宽/高）'}
                                  </Text>
                                </div>
                                <div className="editor-item">
                                  <Text type="secondary">图 3 风格权重 (%)</Text>
                                  <InputNumber disabled={isEditingLocked} min={0} max={100} value={placement.styleWeight} onChange={setPlacementField('styleWeight', 0, 100)} />
                                </div>
                              </div>

                              <div className="editor-thumbs">
                                <div className="editor-thumb">
                                  <Text type="secondary">图 1 门头/现场</Text>
                                  {sceneImage ? <img src={sceneImage} alt="scene-thumb" /> : <div className="thumb-empty">未上传</div>}
                                </div>
                                <div className="editor-thumb">
                                  <Text type="secondary">图 2 平面设计</Text>
                                  {designImage ? <img src={designImage} alt="design-thumb" /> : <div className="thumb-empty">未上传</div>}
                                </div>
                                <div className="editor-thumb">
                                  <Text type="secondary">图 3 效果风格</Text>
                                  {styleImage ? <img src={styleImage} alt="style-thumb" /> : <div className="thumb-empty">未上传</div>}
                                </div>
                              </div>

                              <div className="prompt-summary">{placementHint}</div>
                            </>
                          )
                        }
                      ]}
                    />
                  </div>

                  {showResultPanel && (
                    <div className="result-card">
                      {error && <Alert type="error" message={error} showIcon style={{ marginBottom: 16 }} />}

                      {submitting && !taskInfo && (
                        <div className="empty-state">
                          <Spin />
                          <Text type="secondary">正在提交任务，请稍候...</Text>
                        </div>
                      )}

                      {!submitting && !taskInfo && (
                        <div className="empty-state">
                          <div className="empty-illustration">
                            <div className="img-stack" />
                            <div className="sparkles" />
                          </div>
                          <Text type="secondary">您的生成结果将在这里显示</Text>
                        </div>
                      )}

                      {taskInfo && (
                        <div>
                          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
                            <div className="status-row">
                              <Tag color={taskInfo.status === 'SUCCESS' ? 'green' : taskInfo.status === 'FAILED' ? 'red' : 'blue'}>
                                {taskInfo.status}
                              </Tag>
                              <Text type="secondary">任务 ID: {taskInfo.task_id}</Text>
                            </div>

                            <Progress percent={taskInfo.progress || 0} status={taskInfo.status === 'FAILED' ? 'exception' : 'active'} />

                            {polling && (
                              <div className="loading-row">
                                <Spin />
                                <Text>正在生成中，请稍候...</Text>
                              </div>
                            )}

                            {taskInfo.error_msg && <Alert type="warning" message={taskInfo.error_msg} showIcon />}

                            {taskInfo.output_images && taskInfo.output_images.length > 0 && (
                              <div className="image-grid">
                                {taskInfo.output_images.map((img, index) => {
                                  const imageURL = resolveImageURL(img);
                                  if (!imageURL) return null;
                                  return (
                                    <div key={`${imageURL}_${index}`} className="image-card">
                                      <img src={imageURL} alt="result" />
                                      <div className="image-actions">
                                        <Button
                                          size="small"
                                          icon={<DownloadOutlined />}
                                          onClick={() => handleExportImage(imageURL, 'generated', index)}
                                        >
                                          导出
                                        </Button>
                                      </div>
                                    <div className="image-meta">
                                      <Text>{img.width} x {img.height}</Text>
                                      <Tag color="orange">{img.resolution}</Tag>
                                    </div>
                                  </div>
                                  );
                                })}
                              </div>
                            )}

                            {taskInfo.result && taskInfo.result.length > 0 && (!taskInfo.output_images || taskInfo.output_images.length === 0) && (
                              <div className="image-grid">
                                {taskInfo.result.map((url, index) => (
                                  <div key={`${url}_${index}`} className="image-card">
                                    <img src={url} alt="result" />
                                    <div className="image-actions">
                                      <Button
                                        size="small"
                                        icon={<DownloadOutlined />}
                                        onClick={() => handleExportImage(url, 'generated', index)}
                                      >
                                        导出
                                      </Button>
                                    </div>
                                  </div>
                                ))}
                              </div>
                            )}

                          </Space>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )}

              {currentTab === 'history' && (
                <div className="history-card">
                  <div className="history-header">
                    <Title level={4} style={{ margin: 0 }}>生成历史</Title>
                    <Button onClick={fetchHistory}>刷新</Button>
                  </div>
                  <Table
                    rowKey="id"
                    columns={historyColumns}
                    dataSource={history}
                    loading={historyLoading}
                    className="history-table"
                    scroll={{ x: 920 }}
                    pagination={{ pageSize: 8 }}
                  />
                </div>
              )}
            </section>
          </main>
        </div>
      </div>

      <Modal
        title={authMode === 'login' ? '登录' : '注册'}
        open={authVisible}
        onCancel={() => setAuthVisible(false)}
        footer={null}
      >
        <Form layout="vertical" onFinish={handleAuth} form={authForm}>
          <Form.Item name="phone" label="手机号" rules={[{ required: true, message: '请输入手机号' }]}>
            <Input placeholder="请输入手机号" />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password placeholder="请输入密码" />
          </Form.Item>
          {captchaEnabled && (
            <Form.Item
              name="captcha"
              label="验证码"
              rules={[{ required: true, message: '请输入验证码' }]}
            >
              <Input
                placeholder="请输入验证码"
                addonAfter={(
                  <Button
                    size="small"
                    loading={captchaLoading}
                    disabled={captchaCountdown > 0}
                    onClick={handleSendCaptcha}
                  >
                    {captchaCountdown > 0 ? `${captchaCountdown}s` : '获取验证码'}
                  </Button>
                )}
              />
            </Form.Item>
          )}
          <Space>
            <Button type="primary" htmlType="submit">{authMode === 'login' ? '登录' : '注册'}</Button>
            <Button type="link" onClick={() => setAuthMode(authMode === 'login' ? 'register' : 'login')}>
              {authMode === 'login' ? '没有账号？注册' : '已有账号？登录'}
            </Button>
          </Space>
        </Form>
      </Modal>

      <Modal
        title="积分充值"
        open={rechargeVisible}
        onCancel={() => setRechargeVisible(false)}
        onOk={handleRecharge}
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Text>当前积分：{user ? user.credits : 0}</Text>
          <Space wrap>
            {[20, 50, 100].map((val) => (
              <Button
                key={val}
                type={rechargeAmount === val ? 'primary' : 'default'}
                onClick={() => setRechargeAmount(val)}
              >
                {val} 积分
              </Button>
            ))}
          </Space>
          <InputNumber min={1} value={rechargeAmount} onChange={setRechargeAmount} style={{ width: '100%' }} />
          <Text type="secondary">可选择固定档位或自定义积分。</Text>
        </Space>
      </Modal>

      <Drawer
        title="历史详情"
        open={!!selectedHistory}
        onClose={() => setSelectedHistory(null)}
        width={480}
      >
        {selectedHistory && (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Text>任务 ID：{selectedHistory.task_id}</Text>
            <Text>状态：{selectedHistory.status}</Text>
            <Text>生成时间：{new Date(selectedHistory.created_at).toLocaleString()}</Text>
            <div className="image-grid">
              {selectedHistory.images && selectedHistory.images.length > 0 ? (
                selectedHistory.images.map((url, index) => (
                  <div key={`${url}_${index}`} className="image-card">
                    <img src={url} alt="history" />
                    <div className="image-actions">
                      <Button
                        size="small"
                        icon={<DownloadOutlined />}
                        onClick={() => handleExportImage(url, 'history', index)}
                      >
                        导出
                      </Button>
                    </div>
                  </div>
                ))
              ) : (
                <Text type="secondary">暂无图片</Text>
              )}
            </div>
          </Space>
        )}
      </Drawer>
    </ConfigProvider>
  );
}

export default App;
