import axios from 'axios';

const api = axios.create({
  baseURL: '/api',
  timeout: 180000
});

export async function createTask(payload) {
  const res = await api.post('/tasks', payload, { withCredentials: true });
  return res.data;
}

export async function getTaskStatus(taskId) {
  const res = await api.get(`/tasks/${taskId}`, { withCredentials: true });
  return res.data;
}

export async function waitTask(payload) {
  const res = await api.post('/tasks/wait', payload, { withCredentials: true });
  return res.data;
}

export async function getUsage() {
  const res = await api.get('/usage', { withCredentials: true });
  return res.data;
}

export async function getConfig() {
  const res = await api.get('/config', { withCredentials: true });
  return res.data;
}

export async function login(payload) {
  const res = await api.post('/auth/login', payload, { withCredentials: true });
  return res.data;
}

export async function register(payload) {
  const res = await api.post('/auth/register', payload, { withCredentials: true });
  return res.data;
}

export async function requestCaptcha(payload) {
  const res = await api.post('/auth/captcha', payload, { withCredentials: true });
  return res.data;
}

export async function getMe() {
  const res = await api.get('/auth/me', { withCredentials: true });
  return res.data;
}

export async function logout() {
  const res = await api.post('/auth/logout', {}, { withCredentials: true });
  return res.data;
}

export async function getHistory() {
  const res = await api.get('/history', { withCredentials: true });
  return res.data;
}

export async function recharge(payload) {
  const res = await api.post('/credits/recharge', payload, { withCredentials: true });
  return res.data;
}

export async function presignUpload(payload) {
  const res = await api.post('/uploads/presign', payload, { withCredentials: true });
  return res.data;
}

export async function uploadKieImage(payload) {
  const res = await api.post('/uploads/kie', payload, { withCredentials: true, timeout: 180000 });
  return res.data;
}
