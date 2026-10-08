import unittest
import numpy as np
from yunet_crop import crop_for_faces, select_crop
from unittest.mock import patch


def face(x, y, w, h, confidence=.95):
    return (np.array([x,y,x+w,y+h]),confidence)


class CropTests(unittest.TestCase):
    def check(self, width, height, faces):
        rect, confidence, mode = crop_for_faces(width,height,faces)
        if rect is not None:
            x,y,x1,y1=rect
            self.assertEqual((x1-x)*3,(y1-y)*2)
            self.assertTrue(0<=x<x1<=width and 0<=y<y1<=height)
        return rect,mode

    def test_group_that_fits_keeps_both_faces_and_full_height(self):
        faces=[face(650,100,80,150),face(780,80,80,150)]
        r,mode=self.check(1500,600,faces)
        self.assertEqual(mode,'all-faces')
        self.assertEqual(r[3]-r[1],600)
        for b,_ in faces:
            self.assertTrue(r[0]<=b[0] and r[2]>=b[2] and r[1]<=b[1] and r[3]>=b[3])

    def test_wide_group_uses_contextual_face_not_center_gap(self):
        r,mode=self.check(1500,600,[face(50,100,180,240),face(1200,100,100,150)])
        self.assertEqual(mode,'dominant-face')
        self.assertTrue(r[0]<=50 and r[2]>=230)

    def test_large_closeup_loses_to_face_with_scene_context(self):
        small=face(788,17,172,240)
        r,mode=self.check(1500,500,[face(170,32,380,468),small])
        self.assertEqual(mode,'dominant-face')
        self.assertTrue(r[0]<=788 and r[2]>=960)
        self.assertLessEqual(240/(r[3]-r[1]),2/3)

    def test_no_face_still_renders_portrait(self):
        r,mode=self.check(1500,600,[])
        self.assertEqual(mode,'center-fallback')
        self.assertEqual(r,[550,0,950,600])

    def test_tight_source_stays_unchanged(self):
        r,mode=self.check(100,300,[face(0,0,100,300)])
        self.assertIsNone(r)
        self.assertEqual(mode,'unchanged-closeup')

    def test_face_that_fits_without_scene_context_stays_unchanged(self):
        r,mode=self.check(900,600,[face(300,20,200,450)])
        self.assertIsNone(r)

    def test_retries_smaller_profile_when_confident_face_is_too_large(self):
        large=face(251,84,324,416)
        smaller=face(703,0,150,120,.825)
        clipped=face(2,40,130,310,.847)
        with patch('yunet_crop.detect_faces', side_effect=[[large],[large,smaller,clipped]]) as detector:
            rect, confidence, mode=select_crop(np.zeros((500,1500,3),dtype=np.uint8),'model')
        self.assertIsNotNone(rect)
        self.assertTrue(rect[0]<=703 and rect[2]>=853)
        self.assertEqual(detector.call_count,2)

    def test_retry_does_not_replace_successful_high_confidence_crop(self):
        with patch('yunet_crop.detect_faces',return_value=[face(700,100,100,150)]) as detector:
            rect, confidence, mode=select_crop(np.zeros((500,1500,3),dtype=np.uint8),'model')
        self.assertIsNotNone(rect)
        self.assertEqual(detector.call_count,1)

    def test_portrait_source_preserves_context(self):
        r,mode=self.check(600,1200,[face(40,700,200,250)])
        self.assertIsNotNone(r)


if __name__=='__main__':
    unittest.main()
