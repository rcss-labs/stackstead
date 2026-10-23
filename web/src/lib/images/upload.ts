/**
 * Pairs each picked file with the path it is uploaded under. Folder picks carry
 * webkitRelativePath; plain file picks fall back to the file name. `base` is
 * an optional directory prefix.
 */
export function relativeUploadPaths(files: File[], base?: string): { path: string; blob: File }[] {
  const prefix = base ? base.replace(/\/+$/, '') : '';
  return files.map((blob) => {
    const relative = (blob as File & { webkitRelativePath?: string }).webkitRelativePath || blob.name;
    return { path: prefix ? `${prefix}/${relative}` : relative, blob };
  });
}
