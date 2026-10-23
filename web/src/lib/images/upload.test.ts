import { describe, expect, it } from 'vitest';
import { relativeUploadPaths } from './upload';

function fileAt(name: string, relative?: string): File {
  const file = new File(['x'], name);
  if (relative !== undefined) Object.defineProperty(file, 'webkitRelativePath', { value: relative });
  return file;
}

describe('relativeUploadPaths', () => {
  it('uses the file name when there is no relative path', () => {
    const blob = fileAt('Dockerfile');
    expect(relativeUploadPaths([blob])).toEqual([{ path: 'Dockerfile', blob }]);
  });

  it('uses webkitRelativePath for folder uploads', () => {
    const blob = fileAt('app.py', 'proj/src/app.py');
    expect(relativeUploadPaths([blob])).toEqual([{ path: 'proj/src/app.py', blob }]);
  });

  it('prefixes the base directory', () => {
    const blob = fileAt('Dockerfile');
    expect(relativeUploadPaths([blob], 'docker')).toEqual([{ path: 'docker/Dockerfile', blob }]);
  });
});
