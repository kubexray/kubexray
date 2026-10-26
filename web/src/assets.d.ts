// Vite turns imported assets into URLs.
declare module "*.svg" {
  const url: string;
  export default url;
}
